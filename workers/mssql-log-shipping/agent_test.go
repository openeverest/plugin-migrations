package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDecodeOptions(t *testing.T) {
	options, err := decodeOptions(json.RawMessage(`{"databases":[" shop ","SHOP","orders"],"copyLogins":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(options.Databases, ",") != "shop,orders" || options.LogBackupIntervalSeconds != defaultLogBackupInterval || !options.CopyLogins {
		t.Fatalf("options = %+v", options)
	}
	for _, raw := range []string{``, `{}`, `{"databases":["tempdb"]}`, `{"databases":["a]b"]}`, `{"databases":["a"],"x":1}`,
		`{"databases":["a"],"logBackupIntervalSeconds":301}`, `{"databases":["a"]} {}`} {
		if _, err := decodeOptions(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: expected an error", raw)
		}
	}
}

func TestParseEndpointAndDSN(t *testing.T) {
	endpoint, err := parseEndpoint("sqlserver://sa:p%23ss@orders-primary.shop.svc:1433?encrypt=true&TrustServerCertificate=true")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "orders-primary.shop.svc" || endpoint.Password != "p#ss" || !endpoint.TrustServerCertificate || endpoint.Encrypt != "true" {
		t.Fatalf("endpoint = %+v", endpoint)
	}
	pinned := endpoint.WithHost("orders-1.orders.shop.svc.cluster.local")
	dsn, _ := url.Parse(pinned.dsn())
	query := dsn.Query()
	if dsn.Host != "orders-1.orders.shop.svc.cluster.local:1433" || query.Get("hostNameInCertificate") != "orders-primary.shop.svc" ||
		query.Get("connection timeout") != "0" || query.Get("database") != "master" {
		t.Fatalf("dsn = %s", dsn.Redacted())
	}
	if same := endpoint.WithHost("ORDERS-PRIMARY.shop.svc"); same.HostNameInCertificate != "" {
		t.Fatal("pinning to the same host must not change the certificate name")
	}
	for _, raw := range []string{"mysql://a:b@h", "sqlserver://h:1433", "sqlserver://a:b@h?encrypt=maybe"} {
		if _, err := parseEndpoint(raw); err == nil {
			t.Errorf("%s: expected an error", raw)
		}
	}
}

func TestStorageURLs(t *testing.T) {
	storage, err := newStorage("https://minio.minio.svc.cluster.local", "migrations", "us-east-1", true, "key", "secret", "", "mig-ab12")
	if err != nil {
		t.Fatal(err)
	}
	if storage.CredentialName() != "s3://minio.minio.svc.cluster.local/migrations/openeverest-migrations/mig-ab12" {
		t.Fatalf("credential = %s", storage.CredentialName())
	}
	if got := storage.URL(logBackupKey("shop", 12)); got != storage.CredentialName()+"/shop/log-00000012.trn" {
		t.Fatalf("url = %s", got)
	}
	if fullBackupKey("shop", 1, 1) != "shop/full.bak" || fullBackupKey("shop", 2, 3) != "shop/full-02of03.bak" {
		t.Fatal("unexpected full backup keys")
	}
	virtual, err := newStorage("https://s3.eu-west-1.amazonaws.com:443", "bucket", "eu-west-1", false, "key", "secret", "", "mig-ab12")
	if err != nil {
		t.Fatal(err)
	}
	if virtual.CredentialName() != "s3://bucket.s3.eu-west-1.amazonaws.com:443/openeverest-migrations/mig-ab12" {
		t.Fatalf("credential = %s", virtual.CredentialName())
	}
	for _, endpoint := range []string{"http://minio:9000", "https://minio/path", "minio:9000"} {
		if _, err := newStorage(endpoint, "b", "", true, "k", "s", "", "m"); err == nil {
			t.Errorf("%s: expected an error", endpoint)
		}
	}
	if _, err := newStorage("https://minio", "b", "", true, "k:x", "s", "", "m"); err == nil {
		t.Error("a colon in the access key ID must be rejected")
	}
}

func TestStatements(t *testing.T) {
	backup := backupStatement(backupParams{Database: "sh]op", URLs: []string{"s3://h/b/k1", "s3://h/b/k'2"}, CopyOnly: true, Compress: true,
		Region: "us-east-1"})
	want := `BACKUP DATABASE [sh]]op] TO URL = N's3://h/b/k1', URL = N's3://h/b/k''2' WITH FORMAT, COMPRESSION, ` +
		`COPY_ONLY, BACKUP_OPTIONS = '{"s3": {"region":"us-east-1"}}'`
	if backup != want {
		t.Fatalf("backup:\n%s\nwant:\n%s", backup, want)
	}
	tail := backupStatement(backupParams{Database: "shop", URLs: []string{"s3://h/b/l"}, Log: true, NoRecovery: true})
	if tail != `BACKUP LOG [shop] TO URL = N's3://h/b/l' WITH FORMAT, NORECOVERY` {
		t.Fatalf("tail = %s", tail)
	}
	restore := restoreStatement(restoreParams{Database: "shop", URLs: []string{"s3://h/b/f"}, Replace: true,
		Moves: []fileMove{{"shop", "/var/opt/mssql/data/shop_shop.mdf"}}})
	if restore != `RESTORE DATABASE [shop] FROM URL = N's3://h/b/f' WITH MOVE N'shop' TO N'/var/opt/mssql/data/shop_shop.mdf', REPLACE, NORECOVERY` {
		t.Fatalf("restore = %s", restore)
	}
	credential := credentialStatement("s3://h/b/p", "key", "se'cret", true)
	if credential != `ALTER CREDENTIAL [s3://h/b/p] WITH IDENTITY = 'S3 Access Key', SECRET = N'key:se''cret'` {
		t.Fatalf("credential = %s", credential)
	}
}

func TestLSNLess(t *testing.T) {
	cases := []struct {
		a, b string
		less bool
	}{
		{"9000000001200001", "10000000001200001", true},
		{"37000000034400001", "37000000034400002", true},
		{"37000000034400002", "37000000034400002", false},
		{"", "1", true},
	}
	for _, c := range cases {
		if lsnLess(c.a, c.b) != c.less {
			t.Errorf("lsnLess(%s, %s) != %v", c.a, c.b, c.less)
		}
	}
}

func TestPlanMoves(t *testing.T) {
	files := []map[string]string{
		{"logicalname": "shop", "physicalname": `C:\Data\shop.mdf`, "type": "D"},
		{"logicalname": "shop log", "physicalname": "/var/opt/mssql/data/shop_log.ldf", "type": "L"},
		{"logicalname": "shop/2", "physicalname": "/data/shop2", "type": "D"},
		{"logicalname": "shop_2", "physicalname": "/data/x.ndf", "type": "D"},
	}
	moves := planMoves("shop", files, "/var/opt/mssql/data/", "/var/opt/mssql/log")
	want := []string{
		"/var/opt/mssql/data/shop_shop.mdf", "/var/opt/mssql/log/shop_shop_log.ldf",
		"/var/opt/mssql/data/shop_shop_2.ndf", "/var/opt/mssql/data/shop_shop_2_2.ndf",
	}
	for i, move := range moves {
		if move.PhysicalPath != want[i] || move.LogicalName != files[i]["logicalname"] {
			t.Errorf("move %d = %+v, want %s", i, move, want[i])
		}
	}
}

func TestEndpointHost(t *testing.T) {
	if host := endpointHost("TCP://orders-0.orders.shop.svc.cluster.local:5022"); host != "orders-0.orders.shop.svc.cluster.local" {
		t.Fatalf("host = %q", host)
	}
	if endpointHost("") != "" {
		t.Fatal("empty endpoint must have no host")
	}
}

func source(version string, major int) serverFacts {
	return serverFacts{Major: major, Version: version, Sysadmin: true, Name: "legacy-0", StartedAt: "2026-11-09T10:00:00"}
}

func TestCheckPreflight(t *testing.T) {
	options := Options{Databases: []string{"shop", "orders", "missing", "crm", "secret"}}
	src := sourceFacts{
		Server: source("16.0.4210.1", 16),
		Databases: []databaseFacts{
			{Name: "shop", State: "ONLINE", RecoveryModel: "FULL"},
			{Name: "Orders", State: "ONLINE", RecoveryModel: "SIMPLE"},
			{Name: "crm", State: "OFFLINE", RecoveryModel: "FULL"},
			{Name: "secret", State: "ONLINE", RecoveryModel: "FULL", Encrypted: true, Thumbprint: "0xAB"},
		},
		Recent: []foreignBackup{{Database: "shop", Device: "s3://other/log.trn", FinishedAt: "2026-11-09 09:00:00"}},
	}
	tgt := targetFacts{
		Server:   serverFacts{Major: 15, Version: "15.0.4415.2", Sysadmin: false, Name: "orders-0", StartedAt: "x", Hadr: true, Role: "SECONDARY"},
		Existing: []databaseFacts{{Name: "shop"}},
	}
	problems := strings.Join(checkPreflight(src, tgt, options), "\n")
	for _, want := range []string{
		"the target runs SQL Server 15.0.4415.2; backing up to S3 needs SQL Server 2022",
		"the target user must be a member of the sysadmin role",
		"secondary replica",
		"databases not found on the source: missing",
		"databases not online on the source: crm (OFFLINE)",
		"FULL recovery model, which log backups need: orders",
		"transparent data encryption; create the certificate that protects their key on the target first: secret",
		"already exist on the target: shop",
		"log backups were taken on the source in the last 24 hours (shop to s3://other/log.trn at 2026-11-09 09:00:00)",
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("missing problem %q in:\n%s", want, problems)
		}
	}

	ok := targetFacts{Server: serverFacts{Major: 17, Version: "17.0.1000.7", Sysadmin: true, Name: "orders-0", StartedAt: "y"},
		Certificates: []string{"0xAB"}}
	clean := Options{Databases: []string{"shop", "secret"}, IgnoreRecentLogBackups: true}
	if problems := checkPreflight(src, ok, clean); len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	older := src
	older.Server = source("17.0.1000.7", 17)
	if problems := strings.Join(checkPreflight(older, targetFacts{Server: source("16.0.1", 16)}, clean), "\n"); !strings.Contains(problems, "older than the source") ||
		!strings.Contains(problems, "same server") {
		t.Fatalf("problems = %s", problems)
	}
	inAG := clean
	inAG.LeaveSourceRestoring = true
	src.Databases[0].InAG = true
	if problems := strings.Join(checkPreflight(src, ok, inAG), "\n"); !strings.Contains(problems, "cannot be left restoring") {
		t.Fatalf("problems = %s", problems)
	}
	recreate := Options{Databases: []string{"shop"}, RecreateDatabases: true, IgnoreRecentLogBackups: true}
	agTarget := ok
	agTarget.Existing = []databaseFacts{{Name: "shop", InAG: true}}
	if problems := strings.Join(checkPreflight(src, agTarget, recreate), "\n"); !strings.Contains(problems, "remove them from it") {
		t.Fatalf("problems = %s", problems)
	}
	agTarget.Existing[0].InAG = false
	if problems := checkPreflight(src, agTarget, recreate); len(problems) != 0 {
		t.Fatalf("recreate must accept existing databases: %v", problems)
	}
}

func at(seconds int) *time.Time {
	t := time.Date(2026, 11, 9, 10, 0, seconds, 0, time.UTC)
	return &t
}

func TestLagAndCaughtUp(t *testing.T) {
	clock := *at(30)
	local := time.Now()
	databases := []databaseState{
		{Name: "shop", Stage: dbShipping, Seq: 3, Applied: true, AppliedAt: at(25)},
		{Name: "orders", Stage: dbShipping, Seq: 2, Applied: false, AppliedAt: at(18)},
	}
	if lag := lagSeconds(databases, clock, local, local.Add(2*time.Second)); lag != 14 {
		t.Fatalf("lag = %d, want 14", lag)
	}
	if !caughtUp(databases, 14) || caughtUp(databases, 16) {
		t.Fatal("caught up must follow the lag")
	}
	fresh := []databaseState{{Name: "shop", Stage: dbShipping, Seq: 1, Applied: false, AppliedAt: at(29)}}
	if caughtUp(fresh, 1) {
		t.Fatal("a database without a restored log backup has not caught up")
	}
	if caughtUp([]databaseState{{Stage: dbCopying}}, 0) || caughtUp(nil, 0) {
		t.Fatal("copying databases have not caught up")
	}
	if lagSeconds(databases, time.Time{}, local, local) != 0 {
		t.Fatal("no clock sample means no lag yet")
	}
}

func TestAllFinal(t *testing.T) {
	databases := []databaseState{{Stage: dbRecovered}, {Stage: dbShipping, Final: true, Applied: true}}
	if !allFinal(databases) {
		t.Fatal("expected all final")
	}
	databases = append(databases, databaseState{Stage: dbShipping, Final: true})
	if allFinal(databases) {
		t.Fatal("an unrestored final backup is not final")
	}
}

func TestCreateLoginStatement(t *testing.T) {
	statement := createLoginStatement(sqlLogin{Name: "app]user", SID: []byte{0xab, 0x01}, PasswordHash: []byte{0x02, 0x00, 0xff},
		CheckPolicy: false, CheckExpiration: true}, "shop")
	want := "CREATE LOGIN [app]]user] WITH PASSWORD = 0x0200FF HASHED, SID = 0xAB01, DEFAULT_DATABASE = [shop], CHECK_POLICY = OFF, CHECK_EXPIRATION = OFF"
	if statement != want {
		t.Fatalf("statement = %s", statement)
	}
}

func TestWithoutFinishedMigrations(t *testing.T) {
	base := "s3://minio/migrations/openeverest-migrations/"
	backups := []foreignBackup{
		{Database: "shop", Device: "/var/opt/mssql/data/shop.trn"},
		{Database: "shop", Device: base + "mig-done/shop/log-00000003.trn"},
		{Database: "shop", Device: base + "mig-live/shop/log-00000007.trn"},
		{Database: "shop", Device: "s3://minio/other/openeverest-migrations"},
	}
	kept := withoutFinishedMigrations(backups, []string{base + "mig-live"})
	if len(kept) != 3 || kept[0].Device != backups[0].Device || kept[1].Device != backups[2].Device || kept[2].Device != backups[3].Device {
		t.Fatalf("kept = %+v", kept)
	}
	if migrationCredential(base+"mig-live/shop/full.bak") != base+"mig-live" || migrationCredential("C:\\b.trn") != "" {
		t.Fatal("unexpected credential names")
	}
}

func TestOrphanedUsers(t *testing.T) {
	users := []mappedUser{
		{SID: []byte{1}, Owner: true},
		{SID: []byte{2}, Users: []string{"shop.app", "crm.app"}},
		{SID: []byte{3}, Users: []string{"shop.report"}, Owner: true},
	}
	orphans := orphanedUsers(users, []sqlLogin{{Name: "report", SID: []byte{3}}})
	if strings.Join(orphans, ",") != "shop.app,crm.app" {
		t.Fatalf("orphans = %v", orphans)
	}
}

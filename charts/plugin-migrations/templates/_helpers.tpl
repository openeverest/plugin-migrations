{{- define "plugin-migrations.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "plugin-migrations.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "plugin-migrations.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "plugin-migrations.labels" -}}
helm.sh/chart: {{ include "plugin-migrations.chart" . }}
{{ include "plugin-migrations.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "plugin-migrations.selectorLabels" -}}
app.kubernetes.io/name: {{ include "plugin-migrations.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* Namespace for migration records and workers. */}}
{{- define "plugin-migrations.workloadNamespace" -}}
{{- default .Release.Namespace .Values.workload.namespace -}}
{{- end }}

{{- define "plugin-migrations.workloadRoleName" -}}
{{- printf "%s-workload" (include "plugin-migrations.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

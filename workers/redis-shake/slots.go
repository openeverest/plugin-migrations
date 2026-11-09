package main

import (
	"strconv"
	"sync"
)

const clusterSlots = 16384

// crc16 is the CRC16-XMODEM checksum Redis and Valkey hash keys with.
func crc16(data string) uint16 {
	var crc uint16
	for i := 0; i < len(data); i++ {
		crc ^= uint16(data[i]) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// keySlot is the cluster slot of key, honoring {hash tags}.
func keySlot(key string) int {
	for start := 0; start < len(key); start++ {
		if key[start] != '{' {
			continue
		}
		for end := start + 1; end < len(key); end++ {
			if key[end] == '}' {
				if end > start+1 {
					key = key[start+1 : end]
				}
				return int(crc16(key)) % clusterSlots
			}
		}
		break
	}
	return int(crc16(key)) % clusterSlots
}

var (
	slotTagsOnce sync.Once
	slotTags     [clusterSlots]string
)

// slotTag returns a short string that hashes to slot, so a key containing
// "{tag}" lands on the shard owning that slot.
func slotTag(slot int) string {
	slotTagsOnce.Do(func() {
		found := 0
		for i := 0; found < clusterSlots; i++ {
			tag := strconv.FormatInt(int64(i), 36)
			if slot := int(crc16(tag)) % clusterSlots; slotTags[slot] == "" {
				slotTags[slot] = tag
				found++
			}
		}
	})
	return slotTags[slot]
}

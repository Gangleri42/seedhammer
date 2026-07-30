package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The provisioning record is bookkeeping, never authority: every run
// re-reads the attached board and the record only adds recognition
// ("you plugged in the other one") on top. It holds no secret.
//
// It is one file per key directory with one entry per board, keyed by
// the chip id read from OTP rows 0x000/0x004, which exist on every
// RP2350 including virgin parts.

const recordName = "sh2key.json"

type boardRecord struct {
	ChipID                string `json:"chip_id"`
	RandID                string `json:"rand_id"`
	Slot                  int    `json:"slot"`
	Population            string `json:"population"`
	KeyFingerprint        string `json:"key_fingerprint"`
	SecureBootEnabledByUs bool   `json:"secure_boot_enabled_by_us"`
	Provisioned           string `json:"provisioned"`
}

type recordFile struct {
	Boards []boardRecord `json:"boards"`
}

func recordPath(keyPath string) string {
	return filepath.Join(filepath.Dir(keyPath), recordName)
}

func loadRecords(keyPath string) (*recordFile, error) {
	data, err := os.ReadFile(recordPath(keyPath))
	if errors.Is(err, fs.ErrNotExist) {
		return &recordFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var rf recordFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("%s: %w", recordPath(keyPath), err)
	}
	return &rf, nil
}

func (rf *recordFile) find(chipID string) *boardRecord {
	if chipID == "" {
		return nil
	}
	for i := range rf.Boards {
		if rf.Boards[i].ChipID == chipID {
			return &rf.Boards[i]
		}
	}
	return nil
}

func (rf *recordFile) upsert(r boardRecord) {
	if old := rf.find(r.ChipID); old != nil {
		*old = r
		return
	}
	rf.Boards = append(rf.Boards, r)
}

func (rf *recordFile) save(keyPath string) error {
	data, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(recordPath(keyPath), append(data, '\n'), 0o644)
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

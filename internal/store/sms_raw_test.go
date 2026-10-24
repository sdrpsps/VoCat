package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMigration28PreservesPocketIDSessionsAndAddsSMSArchive(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fork-v27.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for version := 1; version <= 27; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("schema %d: %v", version, err)
			}
		}
	}
	for _, statement := range []string{
		`PRAGMA user_version = 27`,
		`INSERT INTO admins (id, username, created_at, updated_at) VALUES (1, 'pocket-id', 1, 1)`,
		`INSERT INTO sessions (token_hash, admin_id, csrf_hash, expires_at, created_at, oidc_issuer, oidc_subject, oidc_username)
		 VALUES (X'01', 1, X'02', 4102444800, 1, 'https://id.example.test', 'alice-subject', 'alice')`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db := openTestStore(t, path)
	session, err := db.SessionByTokenHash(ctx, []byte{1})
	if err != nil || session.OIDCIssuer != "https://id.example.test" || session.OIDCSubject != "alice-subject" || session.OIDCUsername != "alice" {
		t.Fatalf("Pocket ID session changed during migration: %+v, %v", session, err)
	}
	record := SMSRawRecord{DeviceID: "ec20", ICCID: "card-a", Storage: "ME", Slot: 7, RawPDU: "AABB"}
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	if _, err := reopened.SessionByTokenHash(ctx, []byte{1}); err != nil {
		t.Fatalf("session lost after reopen: %v", err)
	}
	records, err := reopened.ListSMSRawRecords(ctx, "ec20", 10)
	if err != nil || len(records) != 1 || records[0].RawPDU != "AABB" || records[0].ICCID != "card-a" {
		t.Fatalf("raw SMS archive did not persist: %+v, %v", records, err)
	}
}

func TestSMSRawArchiveSurvivesRestartRescanAndSlotReuse(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sms.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	record := SMSRawRecord{DeviceID: "ec20", ModemIMEI: "imei", ICCID: "card-a", IMSI: "imsi-a",
		Storage: "ME", Slot: 7, RawPDU: "aabb", RawUserData: "BB", DecodeError: "truncated"}
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	before, err := db.ListSMSRawRecords(ctx, "ec20", 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("archive: %v, %v", before, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// A decoder upgrade can update parser status, but a different active SIM
	// must not relabel an existing physical record.
	record.ICCID, record.IMSI, record.DecodeError = "card-b", "imsi-b", ""
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	rescanned, err := db.ListSMSRawRecords(ctx, "ec20", 10)
	if err != nil || len(rescanned) != 1 || rescanned[0].ID != before[0].ID ||
		rescanned[0].FirstSeen != before[0].FirstSeen || rescanned[0].ICCID != "card-a" ||
		rescanned[0].IMSI != "imsi-a" || rescanned[0].DecodeError != "" || rescanned[0].RawPDU != "AABB" {
		t.Fatalf("rescan: %#v, %v", rescanned, err)
	}
	record.RawPDU = "CCDD"
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Storage = "SM"
	if err := db.SaveSMSRawRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	all, err := db.ListSMSRawRecords(ctx, "ec20", 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("slot reuse/storage separation: %#v, %v", all, err)
	}
	if filtered, err := db.ListSMSRawRecords(ctx, "other", 10); err != nil || len(filtered) != 0 {
		t.Fatalf("device filter: %v, %v", filtered, err)
	}
}

func TestConcatDecodeErrorsSurviveOtherSegmentsAndClearAfterRecovery(t *testing.T) {
	first := json.RawMessage(`{"concat":{"reference":1,"total":2,"sequence":1},"decode_error":"truncated"}`)
	second := json.RawMessage(`{"concat":{"reference":1,"total":2,"sequence":2}}`)
	_, extra, _, err := mergeConcatSegment(nil, "", first)
	if err != nil {
		t.Fatal(err)
	}
	body, extra, _, err := mergeConcatSegment(extra, "second", second)
	if err != nil || body != "second" || SMSDecodeError(extra) != "part 1: truncated" {
		t.Fatalf("lost first segment's error: %q %s %v", body, extra, err)
	}
	recovered := json.RawMessage(`{"concat":{"reference":1,"total":2,"sequence":1}}`)
	body, extra, changed, err := mergeConcatSegment(extra, "first ", recovered)
	if err != nil || !changed || body != "first second" || SMSDecodeError(extra) != "" {
		t.Fatalf("recovery: %q %s changed=%t err=%v", body, extra, changed, err)
	}
}

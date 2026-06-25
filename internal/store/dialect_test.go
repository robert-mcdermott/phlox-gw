package store

import "testing"

func TestRebindPostgres(t *testing.T) {
	query := `
		SELECT *
		FROM request_log
		WHERE user_id = ? AND note = '?' AND raw = "literal?"
		  -- ignored ?
		  AND model_route = ?
		  /* ignored ? */
		  AND endpoint LIKE ?`

	got := rebindPostgres(query)
	want := `
		SELECT *
		FROM request_log
		WHERE user_id = $1 AND note = '?' AND raw = "literal?"
		  -- ignored ?
		  AND model_route = $2
		  /* ignored ? */
		  AND endpoint LIKE $3`
	if got != want {
		t.Fatalf("unexpected rebound query:\nwant: %s\n got: %s", want, got)
	}
}

func TestOpenWithOptionsRejectsUnsupportedDriver(t *testing.T) {
	_, err := OpenWithOptions(OpenOptions{Driver: "mysql", Path: "ignored"})
	if err == nil {
		t.Fatal("expected unsupported driver error")
	}
}

package store

// Login history and sent alerts (OPS-01 wave 2).

import "time"

const (
	loginKeepDays = 90
	alertKeep     = 200
)

// Login is one sign-in attempt of a user: result ok, password, code, locked or recovery (ok with a recovery code).
type Login struct {
	IP, Agent, Result string
	At                time.Time
	NewIP             bool // a successful sign-in from an address not used in the 90 days before
}

// RecordLogin stores one attempt and drops attempts older than 90 days. It reports whether a successful sign-in
// came from an address this user has not signed in from in that time, while the user had signed in before.
func (s *Store) RecordLogin(orgID, userID int64, ip, agent, result string, at time.Time) (newIP bool, err error) {
	since := at.Add(-loginKeepDays * 24 * time.Hour).Unix()
	if result == "ok" || result == "recovery" {
		var before, here int
		_ = s.DB.QueryRow(`SELECT count(*), coalesce(sum(ip=?),0) FROM logins WHERE user_id=? AND result IN ('ok','recovery') AND at>=?`, ip, userID, since).Scan(&before, &here)
		newIP = before > 0 && here == 0
	}
	if len(agent) > 200 {
		agent = agent[:200]
	}
	if _, err = s.DB.Exec(`INSERT INTO logins(org_id,user_id,ip,agent,result,new_ip,at) VALUES(?,?,?,?,?,?,?)`, orgID, userID, ip, agent, result, b2i(newIP), at.Unix()); err != nil {
		return false, err
	}
	_, err = s.DB.Exec(`DELETE FROM logins WHERE at<?`, since)
	return newIP, err
}

// Logins returns a user's latest attempts, newest first.
func (s *Store) Logins(userID int64, limit int) ([]Login, error) {
	rows, err := s.DB.Query(`SELECT ip,agent,result,new_ip,at FROM logins WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Login
	for rows.Next() {
		var l Login
		var at int64
		var n int
		if err := rows.Scan(&l.IP, &l.Agent, &l.Result, &n, &at); err != nil {
			return nil, err
		}
		l.At, l.NewIP = time.Unix(at, 0), n == 1
		out = append(out, l)
	}
	return out, rows.Err()
}

// SentAlert is one alert the panel sent (or tried to).
type SentAlert struct {
	Kind, Text, Sent string
	At               time.Time
}

// AddAlert records an alert and keeps the latest 200 of the organisation.
func (s *Store) AddAlert(orgID int64, kind, text, sent string, at time.Time) error {
	if _, err := s.DB.Exec(`INSERT INTO alerts(org_id,kind,text,sent,at) VALUES(?,?,?,?,?)`, orgID, kind, text, sent, at.Unix()); err != nil {
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM alerts WHERE org_id=? AND id NOT IN (SELECT id FROM alerts WHERE org_id=? ORDER BY id DESC LIMIT ?)`, orgID, orgID, alertKeep)
	return err
}

// Alerts returns the organisation's latest alerts, newest first.
func (s *Store) Alerts(orgID int64, limit int) ([]SentAlert, error) {
	rows, err := s.DB.Query(`SELECT kind,text,sent,at FROM alerts WHERE org_id=? ORDER BY id DESC LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SentAlert
	for rows.Next() {
		var a SentAlert
		var at int64
		if err := rows.Scan(&a.Kind, &a.Text, &a.Sent, &at); err != nil {
			return nil, err
		}
		a.At = time.Unix(at, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// FirstOrg is the organisation of the first admin: server-wide alerts belong to it.
func (s *Store) FirstOrg() (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT coalesce(min(id),0) FROM orgs`).Scan(&id)
	return id, err
}

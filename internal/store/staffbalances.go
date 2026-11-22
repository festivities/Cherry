package store

// AuditSendLocked reports whether an audit row with action and target carries send in its detail: the send id a staff change
// was made under (economy.StaffBalancesAll keeps its send id in the audit row, so no table is needed). Caller holds AccountsMu,
// as it does for the change itself. ErrNoDatabase without a database.
// ponytail: the audit table has no index on detail; the lookup scans the rows of one target (audit_target). Owner-only changes
// are rare, so a table keyed by send id is the upgrade when the all-players rows grow.
func AuditSendLocked(action, target, send string) (bool, error) {
	d := attachedDB()
	if d == nil {
		return false, ErrNoDatabase
	}
	var n int64
	err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COUNT(*) FROM audit WHERE target = ? AND action = ? AND json_extract(detail, '$.send') = ?`,
			target, action, send).Scan(&n)
	})
	return n > 0, err
}

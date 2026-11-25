package store

import "database/sql"

const bandwidthDailyKey = "bandwidth.daily"

// LoadBandwidthState returns the persisted outbound bandwidth JSON blob from cherry.db meta, or nil when no database is
// attached or the key is not present.
func LoadBandwidthState() ([]byte, error) {
	d := attachedDB()
	if d == nil {
		return nil, nil
	}
	var raw string
	err := d.View(func(q Querier) error {
		var err error
		raw, err = metaValue(q, bandwidthDailyKey)
		return err
	})
	if err != nil || raw == "" {
		return nil, err
	}
	return []byte(raw), nil
}

// SaveBandwidthState writes the outbound bandwidth JSON blob into cherry.db meta. It is a no-op when no database is attached.
func SaveBandwidthState(raw []byte) error {
	d := attachedDB()
	if d == nil {
		return nil
	}
	return d.Update(func(tx *sql.Tx) error {
		return setMetaTx(tx, bandwidthDailyKey, string(raw))
	})
}

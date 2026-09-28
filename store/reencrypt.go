package store

import (
	"context"
	"database/sql"

	"journal/keys"
)

func init() {
	dataMigrations["005_accounts.sql"] = reencryptLegacySecrets
}

func reencryptLegacySecrets(ctx context.Context, tx *sql.Tx, deps MigrateDeps) error {
	root, err := keys.NewRoot(deps.Key)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, password_enc, session_enc FROM accounts WHERE secret_scheme='legacy'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id       int64
		password []byte
		session  []byte
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.password, &r.session); err != nil {
			return err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, r := range pending {
		pw, sess, err := wrapLegacy(root, r.password, r.session)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET password_enc=?, session_enc=?, secret_scheme='envelope', key_id=? WHERE id=? AND secret_scheme='legacy'`,
			pw, nullBytes(sess), root.ID[:], r.id); err != nil {
			return err
		}
	}
	return nil
}

func wrapLegacy(root keys.Root, password, session []byte) ([]byte, []byte, error) {
	var pwOut, sessOut []byte
	if len(password) > 0 && !keys.IsEnvelope(password) {
		plain, err := Unseal(root.Key, password)
		if err != nil {
			return nil, nil, err
		}
		pwOut, err = keys.Seal(root, keys.LabelLegacy, plain)
		if err != nil {
			return nil, nil, err
		}
	} else {
		pwOut = password
	}
	if len(session) > 0 && !keys.IsEnvelope(session) {
		plain, err := Unseal(root.Key, session)
		if err != nil {
			return nil, nil, err
		}
		sessOut, err = keys.Seal(root, keys.LabelToken, plain)
		if err != nil {
			return nil, nil, err
		}
	} else {
		sessOut = session
	}
	return pwOut, sessOut, nil
}

func (s *Store) roots() ([]keys.Root, error) {
	cur, err := keys.NewRoot(s.key)
	if err != nil {
		return nil, err
	}
	out := []keys.Root{cur}
	if len(s.previous) == 32 {
		prev, err := keys.NewRoot(s.previous)
		if err != nil {
			return nil, err
		}
		out = append(out, prev)
	}
	return out, nil
}

func (s *Store) openSecret(sealed []byte, label string) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	if keys.IsEnvelope(sealed) {
		roots, err := s.roots()
		if err != nil {
			return "", err
		}
		b, err := keys.Open(roots, label, sealed)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := Unseal(s.key, sealed)
	if err != nil && len(s.previous) == 32 {
		b, err = Unseal(s.previous, sealed)
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RotateSecrets re-seals every account envelope under the current root.
func (s *Store) RotateSecrets(ctx context.Context) error {
	root, err := keys.NewRoot(s.key)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, password_enc, session_enc FROM accounts`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		id       int64
		password []byte
		session  []byte
	}
	var all []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.password, &it.session); err != nil {
			return err
		}
		all = append(all, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, it := range all {
		pw, err := s.openSecret(it.password, keys.LabelLegacy)
		if err != nil {
			return err
		}
		sess, err := s.openSecret(it.session, keys.LabelToken)
		if err != nil {
			return err
		}
		pwEnc, err := keys.Seal(root, keys.LabelLegacy, []byte(pw))
		if err != nil {
			return err
		}
		var sessEnc []byte
		if sess != "" {
			sessEnc, err = keys.Seal(root, keys.LabelToken, []byte(sess))
			if err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET password_enc=?, session_enc=?, secret_scheme='envelope', key_id=? WHERE id=?`,
			pwEnc, nullBytes(sessEnc), root.ID[:], it.id); err != nil {
			return err
		}
	}
	return nil
}

// SetPrevious records the root that can still decrypt envelopes.
func (s *Store) SetPrevious(key []byte) { s.previous = append([]byte(nil), key...) }

func (s *Store) sealSecret(plaintext, label string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	root, err := keys.NewRoot(s.key)
	if err != nil {
		return nil, err
	}
	return keys.Seal(root, label, []byte(plaintext))
}

package junior

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

type StaffAccess struct {
	AdminID                            int32
	Username, Email                    string
	ExplicitGrant, EffectiveSuperAdmin bool
}

func authorizeJuniorSuperAdmin(ctx context.Context, q DBTX, p Principal, write bool) error {
	if err := Authorize(ctx, q, p, 0, write); err != nil {
		return err
	}
	if p.AdminID <= 0 || p.ReadOnly {
		return ErrForbidden
	}
	var role, username, email string
	err := q.QueryRow(ctx, `SELECT COALESCE(role,'admin'),username,COALESCE(email,'') FROM admin_users WHERE id=$1 AND is_active`, p.AdminID).Scan(&role, &username, &email)
	if err != nil {
		return recordsError(err)
	}
	if role != "super_admin" && !configuredJuniorSuperAdmin(username, email) {
		return ErrForbidden
	}
	return nil
}

func (s *Service) ListStaffAccess(ctx context.Context, p Principal) ([]StaffAccess, error) {
	if err := authorizeJuniorSuperAdmin(ctx, s.DB, p, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT a.id,a.username,COALESCE(a.email,''),COALESCE(a.role,'admin'),EXISTS(SELECT 1 FROM admin_user_permissions permission WHERE permission.admin_user_id=a.id AND permission.permission='junior_manage') FROM admin_users a WHERE a.is_active ORDER BY lower(a.username),a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StaffAccess
	for rows.Next() {
		var item StaffAccess
		var role string
		if err = rows.Scan(&item.AdminID, &item.Username, &item.Email, &role, &item.ExplicitGrant); err != nil {
			return nil, err
		}
		item.EffectiveSuperAdmin = role == "super_admin" || configuredJuniorSuperAdmin(item.Username, item.Email)
		result = append(result, item)
	}
	return result, rows.Err()
}

// SetStaffAccess changes only junior_manage. It neither activates a staff
// account nor grants any senior reporting, sanctions or super-admin capability.
func (s *Service) SetStaffAccess(ctx context.Context, p Principal, targetAdminID int32, grant bool, reason string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = authorizeJuniorSuperAdmin(ctx, tx, p, true); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 300 || strings.IndexFunc(reason, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: provide an access-change reason of at most 300 characters", ErrValidation)
	}
	var target int32
	if err = tx.QueryRow(ctx, `SELECT id FROM admin_users WHERE id=$1 AND is_active FOR SHARE`, targetAdminID).Scan(&target); err != nil {
		return recordsError(err)
	}
	if grant {
		_, err = tx.Exec(ctx, `INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'junior_manage') ON CONFLICT DO NOTHING`, target)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM admin_user_permissions WHERE admin_user_id=$1 AND permission='junior_manage'`, target)
	}
	if err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, 0, "staff_access", int64(target), "junior_staff_access_changed", map[string]any{"permission": "junior_manage", "granted": grant, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

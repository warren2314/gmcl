package sanctions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

func requireIneligibleDecisionApprover(ctx context.Context, tx pgx.Tx, adminID int32) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT sanction_ineligible_decision_approver($1)`, adminID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return errors.New("only a designated ineligible-player decision approver can approve or reject this proposal")
	}
	return nil
}

func finalSignOffNotification(caseID, decisionID int64, reference string) (string, string, string) {
	baseURL := "https://gmcl.co.uk"
	for _, key := range []string{"PUBLIC_BASE_URL", "APP_BASE_URL"} {
		if value := strings.TrimRight(strings.TrimSpace(os.Getenv(key)), "/"); value != "" {
			baseURL = value
			break
		}
	}
	return fmt.Sprintf("case:%d:final-sign-off-request:%d:recipient:", caseID, decisionID),
		"GMCL case " + reference + " ready for final sign-off",
		fmt.Sprintf("Case reference: %s\n\nWaiting for: Denver's final sign-off and issue.\n\nThe decision has been approved and the outcome emails and letters are locked. Review the approved outcome, then select Final sign-off and issue outcomes.\n\nOpen this case:\n%s/admin/cases/%d\n\nYour approval / issue queue:\n%s/admin#my-decisions\n\nThis is a final sign-off request for Denver. The case owner, Dave and Warren do not need to act at this stage. If the outcome has already been issued or the case has been reopened, this request no longer needs action.", reference, baseURL, caseID, baseURL)
}

func queueFinalSignOffNotification(ctx context.Context, tx pgx.Tx, caseID, decisionID int64) error {
	var reference string
	if err := tx.QueryRow(ctx, `SELECT reference FROM sanction_cases WHERE id=$1`, caseID).Scan(&reference); err != nil {
		return err
	}
	key, subject, body := finalSignOffNotification(caseID, decisionID, reference)
	result, err := tx.Exec(ctx, `INSERT INTO sanction_notification_outbox(case_id,decision_revision_id,message_kind,idempotency_key,recipient,subject,body)
		SELECT $1,$2,'final_sign_off_request',$3||LOWER(BTRIM(admin.email)),LOWER(BTRIM(admin.email)),$4,$5
		FROM admin_users admin
		WHERE sanction_ineligible_final_issuer(admin.id) AND BTRIM(admin.email)<>''
		ON CONFLICT(idempotency_key) DO NOTHING`, caseID, decisionID, key, subject, body)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		var queued bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sanction_notification_outbox WHERE case_id=$1 AND decision_revision_id=$2 AND message_kind='final_sign_off_request' AND revoked_at IS NULL)`, caseID, decisionID).Scan(&queued); err != nil {
			return err
		}
		if !queued {
			return errors.New("configure Denver's active final sign-off account before approving this decision")
		}
	}
	return nil
}

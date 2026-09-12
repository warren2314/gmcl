# Ineligible-player approval emails

The handovers use two separate notifications:

1. The investigator reviews the proposed punishment and submits the decision:
   only Dave and Warren receive the decision approval request.
2. Dave or Warren approves and locks the outcome: only Denver receives the
   final sign-off request. Club outcome emails are queued when Denver issues
   the approved outcome.

Case owners such as Steve, Geoff and Tom receive neither handover notification
solely because they own or previously prepared a case. Approval requests are
shown in the dashboard's **Approval / issue queue**. **My ineligible-player
cases** continues to list cases owned by that administrator.

Migration `0089_ineligible_decision_approvers.sql` adds explicit permissions:
`sanctions_ineligible_approve` for Dave's existing Rogers mailbox account and
Warren's account, and `sanctions_ineligible_issue` for `denverthornton`.
The existing approval/publish permissions are still required; super-admin
status alone does not receive these handover requests. Final issue requires the
explicit final-issuer permission and an active Play-Cricket directory entry.
Copy recipients in that directory do not receive final sign-off requests.

The same eligibility checks apply to notifications, the dashboard and actions.
Pending wrongly routed approval alerts are revoked by the migration. The
outbox worker also cancels requests for superseded decisions, completed
stages and recipients whose permissions were removed. Already sent email
cannot be recalled and its audit history remains intact.

Deploy the migration and application together. No notification is resent to
investigators as part of this repair.

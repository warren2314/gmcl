# Cards for a future season

Use **Red cards for a future season (definite)** when the decision requires the cards to take effect in a later season. Use **Suspended red cards (conditional)** when they apply only after a further condition is breached. Reaching a date does not activate a conditional suspension.

The case owner prepares both kinds through **Prepare decision for approval → Decision effects**. The usual owner review, independent approval and Denver final sign-off remain in place.

## Definite award

1. Select the affected team, **Red cards for a future season (definite)**, and the number of cards.
2. Select the configured target season. The form fills its start date; check that date against the decision.
3. Save the proposal and review the calculated points, team, season and date in each outcome version before submitting it for approval.

Three red cards at the start of a season with no existing reds give a six-point deduction (1 + 2 + 3). If the team already has approved reds in that target season, the calculation escalates from that count. The proposal records the complete count and points calculation.

For the Whalley Range outcome, the two current-season points adjustments remain separate: 1st XI **-41**, 2nd XI **-6**. The additional 1st XI effect is **3 definite red cards in 2027**. Both team subjects must be mapped correctly. Do not add a second -6 points adjustment for the three-card award.

Approval reserves the awarded reds and their points in the target season's card ledger. It does not change the original season's totals. Later cards in the target season use this approved opening count. This is a board award and can contain multiple cards without using an ordinary fixture's one-red cap.

One Play-Cricket points task is created for each definite award, assigned through the configured Play-Cricket administrator role. It states the season, count, deduction and effective date. The task cannot be marked complete before that date. The application does not directly update Play-Cricket tables; Denver still applies the adjustment there and records completion. Approval retries do not create duplicate awards or tasks. An overturned decision reverses the target-season ledger and cancels outstanding linked tasks.

## Conditional suspension

Choose **Suspended red cards (conditional)**, record the count, and select a future season and effective date when applicable. A future suspension requires the activation condition; an end/review date may also be entered.

Suspended cards add no red cards or points to the ledger. A follow-up review task retains the condition. Marking that task complete does not activate the cards. Activation requires a separate authorised decision; this change does not introduce automatic activation or a new activation button.

## Season setup and reporting

The future season must exist before it can be selected. The feature does not silently create a season or alter the current competition's defaults. If the form reports that no future season is configured, league administration must arrange the season setup first.

The saved decision, letters, public register, captain timeline and rules lookup retain the effect's target season and card count. The public register supports a scheduled-red filter and labels future effects **Scheduled**. Publication is still required before an award appears publicly. The existing weekly legacy CSV export remains a legacy report and does not include these case-based future awards.

## Validation

Regression tests cover the mixed 2026/2027 decision, current-season isolation, three reds/six points, later-card escalation, conditional cards remaining unapplied, amendment and reversal, duplicate approval, date boundaries including British Summer Time, form row alignment, and task completion before the effective date. The lifecycle test runs against the disposable migrated PostgreSQL database configured through `TEST_DB_DSN` in CI.

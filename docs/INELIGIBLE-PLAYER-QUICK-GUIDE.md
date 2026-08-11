# GMCL ineligible-player work: quick guide

Three routes, one controlled process.

> **Important:** importing is not the same as raising a case. Google Form imports create reports for review. The tracker applies historical information only. A member of staff must deliberately raise every live case.

## Start here

1. Sign in to the GMCL admin portal.
2. Click **Sanctions**.
3. Click **Ineligible-player work**.
4. On **Ineligible-player cases**, choose the route that matches your task.

| What do you need to do? | Choose |
|---|---|
| Review one report and raise its case | **Route 1 - Raise one case** |
| Bring in several Google Form responses and choose which to progress | **Route 2 - Import and choose reports** |
| Reconcile the approved historical workbook | **Route 3 - Import historical tracker** |

### What “raise one case manually” means

An ineligible-player case must start from a private report in the queue. Do not use **Add card, ban, fine or points decision** to create a blank ineligible-player case.

If the report is not in the queue, complete the current private Google Form and follow Route 2.

## Route 1 - Raise one case

1. Click **Sanctions**.
2. Click **Ineligible-player work**.
3. Under **Route 1 - Raise one case**, click **Open next selected report**.
4. If the button says **View reports**, click it and then click **Review report** beside the correct entry.
5. On the intake page, check **Reported details** and any evidence.
6. Under **Raise this case**, check:
   1. **Offending team**
   2. **Reporting club**
   3. **Fixture date**
   4. **Player**
7. Usually no wording change is needed. If necessary, click **Review case wording** and check the recorded allegation and private investigation context.
8. Click **Raise case**.
9. When **Case [reference] is ready** appears, click **Open case**.

**Expected result:** an investigation opens. No email is sent and no outcome is decided.

### If a new case should not be raised

Click **Other outcomes**, then choose the appropriate action:

- Existing investigation: complete **Link to an existing case**, then click **Link and merge intake**.
- Duplicate report: complete **Resolve without a new case**, then click **Mark duplicate**.
- Irrelevant or no action: record the reason, then click **Ignore intake**.

## Route 2 - Import and choose reports

1. Make sure the required reports are present in the private Google Form response sheet. Do not delete, copy or reorder live response rows.
2. Click **Sanctions**.
3. Click **Ineligible-player work**.
4. Under **Route 2 - Import and choose reports**, click **Import and choose reports**.
5. Read the import summary:
   1. **Source rows read** is every response read from Google.
   2. **Added** and **changed** are database updates. Zero is normal when the same sheet is imported again.
   3. **Need attention** means one or more rows have a warning; it does not automatically mean that the row is missing.
6. If the page says **Selection is blocked**, record the import number and error, plus any spreadsheet row shown, then stop for import or identity help.
7. The table contains open, unlinked reports only. Use **Fixture from**, **Fixture to**, **Order** or search to find the current handover.
8. Tick **Progress** beside each required report, or click **Select all shown**. Filtering never unticks a report that is already selected.
9. If a report is missing, click **Open report history** and search for the player. Do not select it again if it is already case-raised, linked, marked duplicate or ignored.
10. Check the selected total against the current handover list. Do not use an old fixed total.
11. Enter a short handover label, for example **Dave handover - 11 Aug 2026**.
12. Click **Save selection and show work queue**.
13. Click **Review report** beside the first selected report and follow Route 1.

**Expected result:** selected reports appear in the normal work queue. Unselected open reports are hidden from that queue, not deleted. Progressed reports remain in **Report history**.

> **One row means one report:** if the Player box contains several names, the chooser shows one checkbox. It does not automatically create one report or case per player. Review that row before raising anything.

> **Important:** the system reads cell values, not blue formatting. Import the full configured sheet and use the selection screen. A new submission remains visible as **New - not yet chosen** until the next selection. Importing or selecting never sends an email, issues a sanction or bulk-creates live cases.

## Route 3 - Import the historical tracker

Use this route only for the approved historical workbook. It must be an `.xlsx` file no larger than 16 MB, with the **Form responses 1** sheet and the expected columns A to Z.

### Step 1 - Upload

1. Click **Sanctions**.
2. Click **Ineligible-player work**.
3. Under **Route 3 - Import historical tracker**, click **Open tracker import**.
4. Under **Step 1 - Upload tracker**, click **Tracker (.xlsx, max 16 MB)**.
5. Select the approved workbook.
6. Click **Upload tracker**.

The system opens **Import check #[number]**.

### Step 2 - Check every row

1. Leave **Needs checking** selected.
2. Review the totals for **Needs checking**, **Suggested matches** and **Needs help**.
3. Check the player, club, team, suggested Google intake and historical state on each row.
4. For a correct straightforward suggestion, click **Confirm suggested match**.
5. Repeat until every straightforward row has moved to **Verified history**.

If a row does not show **Confirm suggested match**, complete its full review:

1. Check **Reconciliation** and the **Google intake ID**.
2. Choose the **Historical case state**.
3. Complete the **Points/cards review** if it appears.
4. Record the review reason and reviewer.
5. Click **Verify row**.

Do not guess. Ask the casework lead to review ambiguous matches or points/cards wording.

### Step 3 - Sign off

When **Needs checking** reaches zero:

1. Find **Step 3 - Sign off**.
2. Check **Your name** and read the confirmation statement.
3. Tick **Save my name and confirmation in the audit history**.
4. Click **Sign off import**.

### Step 4 - Apply history

1. Review the application summary and **Application note**.
2. Tick the one-time application confirmation.
3. Click **Apply signed-off history**.

**Expected result:** signed-off private history and reviewed open/closed status are applied once. Unmatched or excluded rows remain untouched.

> The tracker cannot create a case, decision, sanction, points/cards entry, task, correspondence or email.

## After a live case is raised

1. Click **Open case** and check the evidence and published rule.
2. Save the initial email and reminder, then click **Send initial email to club** when ready.
3. Review the club response and any new evidence.
4. Complete **Prepare decision for approval** and click **Submit decision for approval**.
5. A different authorised administrator checks the proposal and clicks **Approve decision and lock outcomes**.
6. After the final previews, click **Issue approved outcomes**.

Stop and ask the casework lead for help if details conflict, **Selection is blocked**, a needs-attention warning cannot be resolved, a tracker row is ambiguous, or the correct club, team, intake or case cannot be found.

## Safety checks to remember

- Raising a case does not send an email.
- A decision requires approval by a different authorised administrator.
- Outcomes are not sent until **Issue approved outcomes** is selected.

## Board flow: controlled intake to outcome

```mermaid
flowchart TD
    A["Private Google Form responses"] --> B["Import every source row safely"]
    B --> C{"What is the row's current position?"}
    C -->|Open and unlinked| D["Available to choose"]
    C -->|Already progressed| E["Retained in report history or its case"]
    C -->|Identity cannot be matched| F["Selection blocked<br/>Manual help required"]
    D --> G["Save the exact work-list selection"]
    G --> H["Selected reports<br/>Normal work queue"]
    G --> I["Unselected open reports<br/>Hidden, retained and auditable"]
    I --> D
    H --> J["Review one report"]
    J --> K{"What is the correct action?"}
    K -->|New matter| L["Raise a case<br/>No email is sent"]
    K -->|Already exists| M["Link to the existing case"]
    K -->|Duplicate or no action| N["Resolve the report"]
    L --> O["Investigate and contact the club"]
    O --> P["Prepare a decision"]
    P --> Q["Independent approval"]
    Q --> R["Issue approved outcomes"]
```

The board control is simple: every source row is accounted for, only open and unlinked work is offered for selection, progressed reports remain auditable, and only an independently approved decision can produce an outcome.

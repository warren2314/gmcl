# Personas and User Journeys

**Planning baseline:** 26 July 2026
**Status:** Recommended experience model; operational assumptions require validation in interviews

The evidence labels defined in [00-executive-summary.md](00-executive-summary.md) apply throughout this document.

## Design principles

- **Verified fact:** Captain reporting is a team-scoped, mobile-relevant workflow that must remain available while portal capabilities are added (`internal/httpserver/captain.go:123-381`, `internal/httpserver/router.go:86-118`).
- **Recommendation:** Present one product with role-aware navigation, but never merge permissions merely because one person has several roles.
- **Recommendation:** Begin each journey with the user's current appointment and scope, not with an unrestricted record search.
- **Recommendation:** Every official result shows its source, season, effective date, governing rule release and route to correction or appeal.
- **Recommendation:** Save draft work, make deadlines visible, and make failure recovery possible without support intervention where that is safe.
- **Assumption:** Most club officials are volunteers using personal phones as well as desktop computers.
- **Open question:** The research sample, pilot clubs and named operational owners must be agreed before detailed interaction design.

## Personas

| Persona | Responsibilities and needs | Data scope | Principal risks and safeguards |
|---|---|---|---|
| Club Primary Administrator | Establishes the club's account, appoints other club officials, monitors actions and keeps official contacts current | One or more explicitly approved clubs; current and historical seasons | A compromised or stale account has broad club impact. Strong authentication, step-up, dual notification and prompt revocation are required |
| Club Administrator or Secretary | Handles reports, messages, corrections and general club administration | Assigned club; category restrictions may narrow access | Must not amend GMCL-owned official decisions directly or see another club |
| Club Play-Cricket Administrator | Reconciles external identifiers and performs permitted external steps | Assigned club's external references and registration workflow | Portal authority must not be mistaken for Play-Cricket authority |
| Club Junior Secretary | Receives and acknowledges junior competition communications | Assigned club and junior competitions; verified adult contacts only | Avoid unnecessary child data; no safeguarding case access by default |
| Club Safeguarding Officer | Receives a separately routed safeguarding referral where policy permits | Restricted safeguarding service only | Separate authorization, storage, response process and DPIA; no general officer access |
| Team Captain or Manager | Completes existing reports and sees team actions | Appointed team and season | Existing magic-link access must not be broken; appointment expiry must be enforced |
| Read-only Club User | Reviews club information without altering it | Assigned club, excluding restricted categories | Export and attachment access must be separately granted |
| GMCL Super Administrator | Break-glass platform and role administration | League-wide, but still purpose-limited and audited | No routine use of super-admin; step-up and high-severity audit alerts |
| Board or League Administrator | Oversees league operations, decisions and publications | Assigned competitions/seasons or league-wide | Decision races and unpublished drafts require explicit state transitions |
| Club Liaison Officer | Verifies primary administrators and coordinates club cases | Allocated clubs and general case categories | Official-contact evidence must be recorded without excessive retention |
| Compliance and Sanctions Officer | Reviews reports, cards, breaches, sanctions and appeals | Assigned competitions and cases | Preserve team-level ledger, evidence, rule release and decision history |
| Player Registration Officer | Reviews registration applications and transfer evidence | Assigned competitions; restricted player documents | Data minimisation, document access logging and duplicate-decision protection |
| Junior Competition Administrator | Sends competition notices and handles junior administration | Junior competitions and verified adult club recipients | No safeguarding access unless separately appointed |
| Safeguarding Officer | Handles safeguarding routes independently | Explicitly assigned safeguarding cases | Highest restriction, separate support and incident procedures |
| Fixture Administrator | Captures constraints, compares candidates and publishes approved schedules | Assigned competitions/seasons | Solver output is advisory; publication requires a named human approval |
| Read-only Auditor | Inspects records and audit trails | Time-bound, purpose-specific scope | Bulk export, attachments and sensitive fields excluded by default |
| Umpire or approved identity checker | Checks the eligibility identity view at a match | One fixture, short time window, minimum fields | Prevent bulk browsing, photography scraping and use beyond the match |
| Player or responsible adult | Supplies information for a registration journey | Own application only | Identity proofing, age-appropriate notices and external handoff clarity |
| Previous-club responsible officer | Responds to a transfer-clearance request | One signed, expiring request | Current Rule 3.1 direct-email requirement remains authoritative |
| Operations support | Helps users recover access and resolve failed workflows | Minimum metadata needed for support | Support must not become an authentication bypass |

## Journey conventions

Each diagram names the authoritative decision point. A portal state such as `submitted` is not the same as an external Play-Cricket registration or a published GMCL decision. Error responses must use a correlation reference, avoid personal-data disclosure, preserve saved work and record security-relevant failures.

## Journey 1: Club administrator accepts an invitation

```mermaid
sequenceDiagram
    actor Liaison as GMCL Club Liaison Officer
    actor Admin as Prospective Club Administrator
    participant Portal as GMCL Portal
    participant IdP as Managed OIDC Provider
    participant Audit as Audit Service
    Liaison->>Portal: Verify official-contact evidence and create invitation
    Portal->>Admin: Send single-use, short-lived invitation
    Admin->>Portal: Open invitation and confirm club
    Portal->>IdP: Start OIDC enrolment with PKCE
    IdP-->>Admin: Enrol passkey or password plus TOTP
    IdP-->>Portal: Return verified identity claims
    Portal->>Portal: Bind identity to pending membership
    Admin->>Portal: Accept terms and recovery controls
    Portal->>Audit: Record approval, identity, role, scope and evidence reference
    Portal-->>Admin: Activate revocable session and show Action Centre
```

- **Recommendation:** Only a Club Liaison Officer or Super Administrator may approve the first Club Primary Administrator, against verified official-contact evidence. Later appointments follow club approval policy and notify the primary administrator and GMCL.
- **Failure journey:** Expired, already-used or mismatched invitations disclose no account state. Reissue creates a new token and invalidates the old one. An identity already connected to another club may accept the new membership without creating a duplicate user.

## Journey 2: Club reviews missed reports

```mermaid
flowchart TD
    A["Club official opens Reports action"] --> B["Portal authorizes club, team, season and role"]
    B --> C["Show derived requirement with fixture, source and deadline"]
    C --> D{"Submission or approved exemption found?"}
    D -->|Yes| E["Show satisfied source record and audit history"]
    D -->|No| F["Show potential missed-report finding and correction window"]
    F --> G{"Club accepts finding?"}
    G -->|Yes| H["Acknowledge official notice"]
    G -->|No| I["Create correction request with reason and evidence"]
    I --> J["GMCL reviews without overwriting original requirement"]
    J --> K["Decision and effective date added to timeline"]
```

- **Verified fact:** The current calculation creates expected requirements by team and fixture and checks submissions, exemptions and legacy matching (`internal/httpserver/dashboard_data.go:8-116`).
- **Failure journey:** A stale page cannot create a duplicate correction. The API rejects a request for another club without disclosing whether the requirement exists.

## Journey 3: Club replies to a GMCL message

```mermaid
sequenceDiagram
    actor User as Authorized Club User
    participant Inbox as Club Inbox
    participant Case as Case Service
    participant Notify as Notification Service
    participant GMCL as Assigned GMCL Officer
    User->>Inbox: Open case from club Action Centre
    Inbox->>Case: Request club-visible timeline in current scope
    Case-->>Inbox: Visible messages, deadline and permitted actions only
    User->>Case: Post reply and approved attachments
    Case->>Case: Scan attachments and append visible message
    Case->>Notify: Notify owner/watchers without message content
    Notify-->>GMCL: Secure-link email
    Case-->>User: Show sent status and audit reference
```

- **Recommendation:** The club-facing repository never joins the internal-note table. A reply changes `Awaiting club` to `Awaiting GMCL` atomically.
- **Failure journey:** Quarantined attachments are not downloadable or delivered. A departed administrator retains no access even if an old email link is opened.

## Journey 4: GMCL assigns and escalates a club case

```mermaid
flowchart LR
    A["New case in category queue"] --> B["Triage officer validates category and sensitivity"]
    B --> C["Assign primary owner and optional watchers"]
    C --> D["Set priority, response target and Awaiting GMCL"]
    D --> E{"Target missed or risk raised?"}
    E -->|No| F["Officer responds or requests club action"]
    E -->|Yes| G["Escalation rule notifies duty role and records reason"]
    G --> H["Authorized manager reassigns or changes priority"]
    F --> I["Resolve with outcome"]
    H --> I
    I --> J["Club acknowledges; authorized officer closes"]
```

- **Recommendation:** Assignment history is append-only. Reassignment never removes previous ownership evidence.
- **Failure journey:** Safeguarding referrals route to a separate restricted service; a general triage officer sees only a neutral handoff status.

## Journey 5: Club updates its starred-player list

```mermaid
flowchart TD
    A["Club opens current season and rule release"] --> B["Fork new draft from latest approved version"]
    B --> C["Edit entries, exemptions and evidence"]
    C --> D["Deterministic validation checks completeness and deadlines"]
    D --> E{"Blocking validation errors?"}
    E -->|Yes| F["Explain source fields and keep draft"]
    E -->|No| G["Named club official submits immutable version"]
    G --> H["GMCL reviewer compares versions and evidence"]
    H --> I{"Decision"}
    I -->|More information| J["Return to club with questions"]
    J --> B
    I -->|Reject| K["Record reason; approved version remains effective"]
    I -->|Approve| L["Publish new effective version; retain predecessor"]
```

- **External dependency:** The workflow uses the season-specific published Rule 3.5 release; it must not encode 2026 thresholds permanently.
- **Failure journey:** A deadline override requires an authorized GMCL role, step-up, a reason and an audit event.

## Journey 6: Hawk explains a potential starred-player breach

```mermaid
sequenceDiagram
    actor Officer as Authorized Reviewer
    participant Detector as Deterministic Rules Service
    participant Hawk as Hawk AI
    participant Rules as Trusted Rule Corpus
    participant ReadModel as Authorized Tenant Read Model
    participant Decision as Human Review
    Detector->>ReadModel: Evaluate scorecard, effective list and exemptions
    Detector-->>Officer: Create potential finding with inputs
    Officer->>Hawk: Ask for explanation
    Hawk->>Rules: Retrieve exact applicable release and citations
    Hawk->>ReadModel: Read only authorized deterministic finding
    Hawk-->>Officer: Explanation, citations, uncertainty and no decision
    Officer->>Decision: Confirm, dismiss or request evidence
    Decision->>Decision: Record human reason and rule version
```

- **Recommendation:** Hawk cannot create or amend official records, approve, sanction, notify or publish. A deterministic service creates the potential finding; Hawk may explain it.
- **Failure journey:** Prompt injection, unavailable citations or ambiguous player matching produces a refusal or escalation, never a definitive eligibility decision.

## Journey 7: Junior administrator sends a junior communication

```mermaid
sequenceDiagram
    actor Joe as Junior Competition Administrator
    participant Composer as Junior Communications
    participant Directory as Adult Role Directory
    participant Case as Message Case Service
    participant Email as Email Notification
    Joe->>Composer: Select competition, club roles and template
    Composer->>Directory: Resolve verified current adult recipients
    Directory-->>Composer: Recipient count and exclusions, not child list
    Joe->>Composer: Review and confirm notice
    Composer->>Case: Create club-addressed cases and audit selection
    Case->>Email: Send content-free notifications
    Email-->>Joe: Delivery, bounce and acknowledgement summary
```

- **Recommendation:** Initial junior communications target verified adult club roles only. No player-level recipient selection is included.
- **Failure journey:** A safeguarding category cannot be sent through this bulk path; it enters the separate restricted safeguarding route.

## Journey 8: Umpire checks match-day player identity

```mermaid
flowchart TD
    A["Approved official opens time-bound fixture link"] --> B["Step-up or approved match-official authentication"]
    B --> C["Portal verifies appointment, fixture window and purpose"]
    C --> D{"Authorized photo source and current approval?"}
    D -->|Yes| E["Show minimal roster, name, approved photo and status"]
    D -->|No| F["Show non-photo fallback and escalation instruction"]
    E --> G["Record individual access; disable bulk export"]
    F --> G
    G --> H["Access expires automatically after match window"]
```

- **External dependency:** Play-Cricket photo API access, controller roles and redistribution rights are unconfirmed. This journey remains blocked until written ECB/Play-Cricket agreement and DPIA approval.
- **Failure journey:** No photo must not be displayed as a false match or an eligibility failure; the official follows the agreed manual identity route.

## Journey 9: Player completes a registration application

```mermaid
flowchart TD
    A["Player or responsible adult opens signed invitation"] --> B["Age-appropriate privacy notice and identity checks"]
    B --> C["Guided questions reveal only applicable requirements"]
    C --> D["Save draft and validate documents"]
    D --> E["Submit immutable application version"]
    E --> F["Club verifies and performs required Play-Cricket step"]
    F --> G["GMCL Registration Officer reviews"]
    G --> H{"Decision"}
    H -->|More information| C
    H -->|Approved internally| I["Reconcile external Play-Cricket state"]
    H -->|Rejected| J["Record reasons and review route"]
    I --> K["Complete only after required external state is confirmed"]
```

- **Recommendation:** The portal provides one guided status and task list while honestly labelling the Play-Cricket handoff.
- **Failure journey:** A duplicate person or external member ID creates a reconciliation task, not an automatic merge.

## Journey 10: Previous club provides transfer clearance

```mermaid
sequenceDiagram
    actor Applicant as Applicant or New Club
    participant Portal as Registration Case
    participant Officer as Previous Club Responsible Officer
    participant Email as Official Email Channel
    participant GMCL as GMCL Registration Officer
    Applicant->>Portal: Submit transfer details and former club
    Portal->>Email: Issue controlled request and instructions
    Email-->>Officer: Direct response path with case reference
    Officer->>Email: Send direct confirmation to GMCL channel
    GMCL->>Portal: Verify sender and record evidence reference
    Portal-->>Applicant: Update status without exposing private correspondence
```

- **Verified fact:** Published Rule 3.1, updated 16 February 2026, requires a direct email from the responsible former-club officer and says forwarded emails are not accepted.
- **Recommendation:** Keep that direct-email evidence route until the rule is formally amended. The portal may coordinate and display status but must not pretend an in-portal click replaces the mandated email.
- **Failure journey:** An unverifiable or forwarded response remains `clearance required` and is referred to a Registration Officer.

## Journey 11: Fixture administrator generates and approves a candidate

```mermaid
flowchart TD
    A["Import reconciled teams, venues, calendar and historic decisions"] --> B["Validate constraint catalogue and version"]
    B --> C["Authorized administrator starts isolated CP-SAT run"]
    C --> D["Solver creates candidate and objective breakdown"]
    D --> E["Hard-constraint validation"]
    E --> F{"Valid?"}
    F -->|No| G["Reject candidate and diagnose infeasibility"]
    F -->|Yes| H["Compare soft scores and prior manual overrides"]
    H --> I["Administrator edits via recorded overrides"]
    I --> J["Independent approval and step-up"]
    J --> K["Create publication version"]
    K --> L["Controlled publish; never automatic"]
```

- **Recommendation:** OR-Tools CP-SAT is evaluated in an isolated prototype only after process interviews, constraint capture and historical-data preparation.
- **Failure journey:** An infeasible run returns conflicting constraints and preserves the last approved schedule. A solver crash or timeout cannot affect the published fixture set.

## Cross-journey exception requirements

| Exception | Required behaviour |
|---|---|
| Role expires during a draft | Preserve the draft but deny further access; an authorized successor may be explicitly assigned |
| Season changes | Keep the original season and rule release; never reinterpret a historical decision under the current release |
| Concurrent decisions | Use version checks and a database transaction; only one terminal decision succeeds |
| Notification fails | Preserve the portal action, retry safely, surface delivery state and follow the official-email fallback |
| External service unavailable | Queue bounded retries where safe, show a truthful stale/as-at state and provide a manual operational route |
| Attachment fails scanning | Keep it quarantined, notify the uploader without exposing storage details, and allow safe replacement |
| Permission denied | Return an authorized not-found or forbidden response with no foreign metadata and record suspicious patterns |
| Personal data mismatch | Create a restricted reconciliation task; do not silently merge identities or registrations |
| Rule ambiguity | Stop automation, cite the applicable text and assign a human decision owner |

## Research and validation plan

**Recommendation:** Validate these journeys with at least two representatives from club administration, captains, league operations, registration, sanctions/compliance, junior administration and fixture administration, plus safeguarding and data-protection reviewers. Test both desktop administration and representative mobile devices at a ground. Record observed terminology, exception frequency, evidence used, peak workload and accessibility needs.

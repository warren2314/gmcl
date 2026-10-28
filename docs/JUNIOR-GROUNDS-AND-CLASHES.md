# Saved grounds and junior fixture clashes

For the Sunday 1 November 2026 demonstration, all ground addresses, postcodes and layouts are fictional. Real grounds must be checked with clubs before operational use. U9 enhancements are deferred.

## Workflow

1. Open **Grounds and clashes**. Save the venue name, street address/site description, postcode and source/verification note. Postcode validation checks the format only. The saved ground ID identifies a physical venue; names and postcodes alone do not merge venues.
2. Record alternative simultaneous layouts. **2 U11 OR 1 U15** permits either arrangement, but does not permit a U11 and U15 together. Add a verified mixed row explicitly where physically supported. A smaller subset of a layout is allowed.
3. In each competition, choose saved home grounds under dates and constraints. For an individual fixture, any saved venue can be chosen, including a neutral venue belonging to neither club. Duration controls the occupied time; no turnaround buffer is assumed.
4. Inspect the red clash warnings on the competition and the central Grounds page. They identify venue/postcode, date, overlapping time, competing fixtures, age groups, competition names, draft/published state and allowed layouts. Resolve the conflict by changing time or venue. Publication is blocked while blocking issues remain.
5. Check external and senior bookings separately before publishing. There are no automatic clash emails. U9 capacity is outside this release.

Both saved drafts and current publications across junior competitions are reservations. The same match in draft and publication is counted once during its overlap; moving its draft reserves both the old official slot and the proposed new slot until republished. Cross-season junior bookings are checked where dates overlap. An unpublished competition's abandoned draft must be deliberately removed if it should release a booking.

## Legacy and history

Older drafts need a saved ground selected explicitly. Older published labels can be associated through **Link historical venue**; this appends an audited link without editing their immutable publication rows. Once a ground directory exists, unlinked non-U9 bookings on a relevant date block publication until identified. No automatic matching by name or postcode is performed in normal operation.

New publications retain the venue ID, name, address, postcode and reviewed rule details. Subsequent rule edits affect current availability checks but do not rewrite the published venue snapshot. Changing a referenced ground increments competition revisions so a previously prepared publication cannot be submitted silently against changed rules. Mutating fixtures, publications and grounds share a transaction lock; publication checks and snapshot creation run atomically.

## Sunday demonstration

Open **Sunday Demonstration Ground** in Grounds and clashes. The two **Venue demonstration — U11** fixtures and the **Venue demonstration — U15** fixture share the neutral venue on **29 May 2027 at 18:00**, for 60 minutes.

The two U11 fixtures fit; adding the U15 triggers a blocking mixed-age warning. In the U15 draft, change its start to **19:00**, save and refresh the Grounds page. The bookings now fit back-to-back. Keep fixtures as drafts unless intentionally demonstrating publication. Saved practice changes persist on restart; the demo upgrade does not reset Joe's existing teams, receipts or fixtures.

The isolated demo update is separate from live production release. PR93 supplies the team-entry base; the grounds change is stacked on it. Production merge/deployment requires the owner's release approval.

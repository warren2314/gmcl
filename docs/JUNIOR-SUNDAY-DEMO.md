# Joe's Sunday demonstration

Sunday **1 November 2026**. Dedicated address: `https://junior-demo.gmcl.co.uk/`. Access details are supplied privately, not stored in this repository.

All clubs, contacts, forms, messages and results are fictional. The demo's own database and files persist between visits; they do not affect the live league system. No real emails are sent. The shared inbox remains explicitly disconnected. Demo access expires at 00:00 UTC on 3 November.

## Before presenting

Open the link on the actual presentation computer and sign in normally as `joe-demo` with the supplied demonstration password. Keep `/admin/demo-guide` available in another tab for the guide and simulated sign-in inbox; both require the normal staff session. Do not use a live GMCL password.

Verify the 2027 DEMONSTRATION season is selected. Start with Oakfield's registration outstanding. Riverside has a response to review, Hillcrest has a completed response, Meadowbrook has a returned response and Oakfield has an overdue ground declaration. Willow Park illustrates an inactive club with retained 2026 history.

## Suggested 10-minute walkthrough

1. **Junior Home:** outstanding work opens its matching records. Explain readiness and season context.
2. **Oakfield Juniors CC:** use section links to show contacts, teams, official forms, competition entries, published fixtures, concessions and correspondence together. Show read-only View as Club before switching to an actual club session.
3. **Contact directory:** filter U13 coaches by competition, export the same directory and select specific people. Preview a message audience. Demonstrate selected clubs and explain address deduplication. Preparing or queuing is not sending.
4. **Fixtures:** U13 has a published six-match schedule; U11 has a six-match draft. Inspect dates, grounds, checks and publication history. Publication and email delivery are separate outcomes.
5. **Concessions:** show the confirmed fictional concession and a separate candidate awaiting review. Explain the imported result evidence, Confirm/Dismiss/Request information and retained decision history. No automatic fine is issued.
6. **Club action:** open Club Junior Lead sign-in, enter `lead1@demo.example.invalid`, then refresh the guide's simulated inbox. Open the new one-use link and confirm it. Submit Oakfield's registration form from the actual club portal.
7. **Staff review:** return to League Admin, open the submitted response and complete or return it with a reason. Show the retained receipt and changed dashboard status.

Normal sign-in links expire after 15 minutes and work once; request another rather than sharing an old link. The simulated inbox exists only in this isolated executable. Requests are throttled for a minute, so wait before requesting repeated links for the same address.

## What to say about scope

The core workflows and four completion features are present. Real clubs still require authoritative roster/contact review, agreed dates and competition setup, verified email/invitations and a small pilot. The mailbox connection is being prepared separately. Results currently use reviewed CSV; there is no connected junior Play-Cricket results feed. Fines and fixture-change negotiation are future enhancements.

## Operations

The dedicated Compose project is `gmcl-junior-demo`, using `docker-compose.junior-demo.yml`. The live `gmcl` project and its volumes must not be used for demo reset. Keep the deployed image identity, binary checksum and proxy configuration backup in the host demo directory. Restarting preserves demonstration progress; resetting is a separate explicit operation and must retain the previous demo volume as recovery.

The demo application service is `junior-demo-app`. Never name this service `app`: Compose adds the service name as a DNS alias on the shared proxy network, which would collide with the production application. After any container recreation, verify that only the production application owns the `app` alias, then check both live and demo login endpoints.

The Caddy addition is a separate hostname only. Validate the complete derived configuration before reloading, retain its prior configuration and verify the live health endpoint afterwards. Restarts preserve the container's configuration; if Caddy is recreated from an older image before the demo, reapply the reviewed hostname fragment. The source Caddyfile includes it for subsequent image builds.

After the event, retain the handoff and decide whether to extend the demo or remove the hostname/project. Expiry blocks interactive access without deleting the demonstration records.

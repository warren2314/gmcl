package httpserver

import (
	"context"
	"testing"

	"cricket-ground-feedback/internal/db"
)

// Explicit fixture IDs and .invalid mailboxes keep these records recognisable.
// The helper's unique migrated schema owns every row and is dropped on cleanup.
func seedAdminWorkspaceHandlerCases(t *testing.T, ctx context.Context, pool *db.Pool) {
	t.Helper()
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `
		INSERT INTO seasons(id,name,start_date,end_date) VALUES(90001,'Synthetic closed season','2026-01-01','2026-09-30');
		INSERT INTO clubs(id,name) VALUES(90001,'Synthetic Offending Club'),(90002,'Synthetic Reporting Club');
		INSERT INTO teams(id,club_id,name) VALUES(90001,90001,'Synthetic First XI');
		INSERT INTO admin_users(id,username,password_hash,email,role,is_active,force_password_change) VALUES
			(90007,'synthetic_owner','test'::bytea,'owner@example.invalid','admin',true,false),
			(90008,'synthetic_reviewer','test'::bytea,'reviewer@example.invalid','admin',true,false),
			(90009,'synthetic_general_approver','test'::bytea,'general@example.invalid','admin',true,false),
			(90010,'synthetic_final_issuer','test'::bytea,'issuer@example.invalid','admin',true,false),
			(90011,'synthetic_triage_only','test'::bytea,'triage@example.invalid','admin',true,false),
			(90012,'synthetic_no_grants','test'::bytea,'no-grants@example.invalid','admin',true,false),
			(90013,'synthetic_password_change','test'::bytea,'password@example.invalid','admin',true,true),
			(90014,'synthetic_proposal_only','test'::bytea,'propose@example.invalid','admin',true,false),
			(90015,'synthetic_approval_only','test'::bytea,'approval-only@example.invalid','admin',true,false);
		INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES
			(90007,'sanctions_triage'),(90007,'sanctions_investigate'),(90007,'sanctions_propose'),
			(90008,'sanctions_investigate'),(90008,'sanctions_propose'),(90008,'sanctions_approve'),(90008,'sanctions_ineligible_approve'),
			(90009,'sanctions_investigate'),(90009,'sanctions_propose'),(90009,'sanctions_approve'),
			(90010,'sanctions_investigate'),(90010,'sanctions_publish'),(90010,'sanctions_ineligible_issue'),
			(90011,'sanctions_triage'),(90013,'sanctions_investigate'),(90014,'sanctions_propose'),(90015,'sanctions_approve');
		INSERT INTO sanction_recipient_directory(id,recipient_role,name,email)
			VALUES(90001,'play_cricket','Synthetic final issuer','issuer@example.invalid');
		INSERT INTO sanction_cases(id,reference,source_type,status,season_id,club_id,reporting_club_id,team_id,assigned_admin_id,proposed_by_admin_id,public_summary,private_summary,player_name,match_date,reporter_name,reporter_email) VALUES
			(910001,'SYNTHETIC-REPLY','ineligible_player','response_pending',90001,90001,90002,90001,90007,NULL,'Synthetic public reply case','Synthetic private reply case','Synthetic Player','2026-07-01','Synthetic Reporter','reporter@example.invalid'),
			(910002,'SYNTHETIC-OWNER-NOTICES','ineligible_player','decision_proposed',90001,90001,90002,90001,90007,90007,'Synthetic notice-review case','Synthetic private notice review','Synthetic Player','2026-07-02',NULL,NULL),
			(910003,'SYNTHETIC-SAME-PROPOSER','ineligible_player','decision_proposed',90001,90001,90002,90001,90008,90008,'Synthetic designated approval case','Synthetic private approval','Synthetic Player','2026-07-03',NULL,NULL),
			(910004,'SYNTHETIC-FINAL-ISSUE','ineligible_player','approved',90001,90001,90002,90001,90007,90007,'Synthetic locked outcome case','Synthetic private locked outcome','Synthetic Player','2026-07-04',NULL,NULL),
			(910005,'SYNTHETIC-SOURCE-CHANGE','ineligible_player','approved',90001,90001,90002,90001,90007,90007,'Synthetic corrected-source case','Synthetic private source review','Synthetic Player','2026-07-05',NULL,NULL),
			(910006,'SYNTHETIC-COMPLETED','ineligible_player','published',90001,90001,90002,90001,90007,90007,'Synthetic completed case','Synthetic private historical case','Synthetic Player','2026-07-06',NULL,NULL),
			(910007,'SYNTHETIC-ORDINARY-PROPOSER','manual','decision_proposed',90001,90001,90002,90001,90009,90009,'Synthetic ordinary approval case','Synthetic private ordinary case','Synthetic Player','2026-07-07',NULL,NULL),
			(910008,'SYNTHETIC-NEW-REVISION','ineligible_player','decision_proposed',90001,90001,90002,90001,90008,90008,'Synthetic amended proposal case','Synthetic private amended case','Synthetic Player','2026-07-08',NULL,NULL),
			(910009,'SYNTHETIC-READ-ONLY','ineligible_player','investigating',90001,90001,90002,90001,90007,NULL,'Synthetic read-only investigation case','Synthetic private investigation','Synthetic Player','2026-07-09',NULL,NULL);
		UPDATE sanction_cases SET public_status='active',published_at=now() WHERE id=910006;
		INSERT INTO sanction_decision_revisions(id,case_id,revision,status,public_reason,proposed_by_admin_id,rule_reference,outcome_findings,appeal_instructions) VALUES
			(920002,910002,1,'proposed','Synthetic reviewed findings',90007,'Synthetic Rule 3.5','Synthetic recorded findings','Synthetic recorded appeal instructions'),
			(920003,910003,1,'proposed','Synthetic reviewed findings',90008,'Synthetic Rule 3.5','Synthetic recorded findings','Synthetic recorded appeal instructions'),
			(920004,910004,1,'approved','Synthetic locked findings',90007,'Synthetic Rule 3.5','Synthetic locked findings','Synthetic recorded appeal instructions'),
			(920005,910005,1,'approved','Synthetic source-review findings',90007,'Synthetic Rule 3.5','Synthetic source-review findings','Synthetic recorded appeal instructions'),
			(920006,910006,1,'approved','Synthetic historical explanation',90007,'Synthetic Rule 3.5','Synthetic historical explanation','Synthetic recorded appeal instructions'),
			(920007,910007,1,'proposed','Synthetic ordinary findings',90009,'Synthetic Rule 1','Synthetic ordinary findings','Synthetic recorded appeal instructions'),
			(920081,910008,1,'proposed','Synthetic superseded findings',90008,'Synthetic Rule 3.5','Synthetic superseded findings','Synthetic recorded appeal instructions');
		INSERT INTO sanction_decision_revisions(id,case_id,revision,status,public_reason,proposed_by_admin_id,rule_reference,outcome_findings,appeal_instructions,supersedes_id)
			VALUES(920082,910008,2,'proposed','Synthetic current findings',90008,'Synthetic Rule 3.5','Synthetic current findings','Synthetic recorded appeal instructions',920081);
		INSERT INTO sanction_effect_revisions(id,decision_revision_id,effect_type,subject_type,status,public_details)
			SELECT 930000+row_number() OVER(ORDER BY id),id,'warning','case','pending','{"explanation":"Synthetic recorded warning"}'
			FROM sanction_decision_revisions WHERE case_id BETWEEN 910002 AND 910008;
		INSERT INTO sanction_case_events(id,case_id,event_type,actor_type,actor_label,reason,metadata) VALUES
			(950001,910001,'party_response','reporter','Synthetic club respondent','Synthetic reply awaiting owner review','{"channel":"email","respondent":"reply@example.invalid"}'),
			(950002,910002,'decision_owner_review_required','system','Synthetic checkpoint','Current decision requires owner review','{"decision_revision_id":"920002"}'),
			(950003,910003,'decision_owner_review_required','system','Synthetic checkpoint','Current decision requires owner review','{"decision_revision_id":"920003"}'),
			(950004,910003,'decision_sent_for_approval','admin','Synthetic reviewer','Current decision submitted','{"decision_revision_id":"920003"}'),
			(950006,910006,'external_response_recorded','admin','Synthetic recorder','Synthetic late historical reply','{"channel":"email"}'),
			(950081,910008,'decision_owner_review_required','system','Synthetic checkpoint','Old review required','{"decision_revision_id":"920081"}'),
			(950082,910008,'decision_sent_for_approval','admin','Synthetic reviewer','Old decision submitted','{"decision_revision_id":"920081"}'),
			(950083,910008,'decision_owner_review_required','system','Synthetic checkpoint','New decision requires owner review','{"decision_revision_id":"920082"}');
		INSERT INTO sanction_correspondence_revisions(id,case_id,decision_revision_id,message_kind,audience,revision,status,subject,body) VALUES
			(940021,910002,920002,'outcome_offending_club','offending_club',1,'draft','Synthetic saved offending subject','Synthetic saved offending notice'),
			(940022,910002,920002,'outcome_reporting_club','reporting_club',1,'draft','Synthetic saved reporting subject','Synthetic saved reporting notice'),
			(940023,910002,920002,'outcome_official','official',1,'draft','Synthetic saved official subject','Synthetic saved official notice'),
			(940031,910003,920003,'outcome_offending_club','offending_club',1,'draft','Synthetic saved offending subject','Synthetic saved offending notice'),
			(940032,910003,920003,'outcome_reporting_club','reporting_club',1,'draft','Synthetic saved reporting subject','Synthetic saved reporting notice'),
			(940033,910003,920003,'outcome_official','official',1,'draft','Synthetic saved official subject','Synthetic saved official notice'),
			(940041,910004,920004,'outcome_offending_club','offending_club',1,'approved','Synthetic locked outcome subject','Synthetic locked outcome wording');
		INSERT INTO sanction_case_evidence(id,case_id,visibility,original_name,media_type,byte_size,storage_key,sha256,uploaded_by_type)
			VALUES(960001,910001,'private','Synthetic evidence.txt','text/plain',20,'synthetic-handler-evidence',repeat('a',64),'admin');
		INSERT INTO sanction_follow_up_tasks(id,case_id,task_type,status,assigned_admin_id,current_note)
			VALUES(970001,910006,'investigation_support','open',90007,'Synthetic follow-up evidence check');
		INSERT INTO sanction_notification_outbox(id,case_id,message_kind,idempotency_key,recipient,subject,body,processed_at)
			VALUES(980001,910006,'outcome_offending_club','synthetic-handler-delivery','club@example.invalid','Synthetic recorded notice','Synthetic recorded notice body',now());
		INSERT INTO sanction_notification_attempts(id,outbox_id,attempt_number,status,error_message)
			VALUES(980002,980001,1,'bounced','Synthetic delivery failure');
		INSERT INTO sanction_intakes(id,origin,external_key,state,latest_revision)
			VALUES(990001,'native_form','synthetic-corrected-source','linked',2);
		INSERT INTO sanction_intake_revisions(id,intake_id,revision,raw_data,raw_sha256) VALUES
			(990011,990001,1,'{"player":"Synthetic Player","reason":"Synthetic original source allegation"}',repeat('b',64)),
			(990012,990001,2,'{"player":"Synthetic Player","reason":"Synthetic corrected source allegation"}',repeat('c',64));
		INSERT INTO sanction_intake_case_links(id,intake_id,case_id,relationship)
			VALUES(990021,990001,910005,'primary');
		INSERT INTO sanction_case_subjects(id,case_id,subject_type,team_id,player_name) VALUES
			(990031,910005,'team',90001,NULL),(990032,910005,'player',NULL,'Synthetic Player');
		INSERT INTO sanction_case_intake_merge_resolutions(id,case_id,intake_id,revision_id,relationship,team_id,team_subject_id,player_subject_id,league_origin)
			VALUES(990041,910005,990001,990011,'primary',90001,990031,990032,true);
	`)
}

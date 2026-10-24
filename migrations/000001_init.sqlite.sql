CREATE TABLE t_user (
 Fid INTEGER PRIMARY KEY AUTOINCREMENT,
 Fuser_id TEXT NOT NULL UNIQUE,
 Fname TEXT NOT NULL DEFAULT '',
 Fpwd_hash TEXT NOT NULL,
 Fstatus INTEGER NOT NULL DEFAULT 1,
 Fmax_conv INTEGER NOT NULL DEFAULT 20,
 Fcreate_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 Fmodify_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE t_auth_session (
 Fid INTEGER PRIMARY KEY AUTOINCREMENT,
 Fsession_hash TEXT NOT NULL UNIQUE,
 Fuser_id TEXT NOT NULL,
 Fexpire_time DATETIME NOT NULL,
 Fcreate_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_auth_session_user ON t_auth_session(Fuser_id);
CREATE TABLE t_conversation (
 Fid INTEGER PRIMARY KEY AUTOINCREMENT,
 Fconv_id TEXT NOT NULL UNIQUE,
 Fuser_id TEXT NOT NULL,
 Ftitle TEXT NOT NULL DEFAULT '',
 Fmodel TEXT NOT NULL,
 Ftemplate TEXT NOT NULL DEFAULT 'react-go-sqlite',
 Fpreview_key TEXT NOT NULL UNIQUE,
 Fagent_session_id TEXT NOT NULL DEFAULT '',
 Fruntime_epoch TEXT NOT NULL DEFAULT '',
 Frt_next_seq INTEGER NOT NULL DEFAULT 0,
 Flast_seq INTEGER NOT NULL DEFAULT 0,
 Fexec_generation INTEGER NOT NULL DEFAULT 0,
 Fworker_owner TEXT,
 Flease_until DATETIME,
 Flstate INTEGER NOT NULL DEFAULT 1,
 Flast_active_time DATETIME NOT NULL,
 Fcreate_time DATETIME NOT NULL,
 Fmodify_time DATETIME NOT NULL
);
CREATE INDEX idx_conversation_user_active ON t_conversation(Fuser_id,Flstate,Flast_active_time);
CREATE TABLE t_message (
 Fid INTEGER PRIMARY KEY AUTOINCREMENT,
 Fmsg_id TEXT NOT NULL UNIQUE,
 Fconv_id TEXT NOT NULL,
 Frun_id TEXT NOT NULL,
 Frole INTEGER NOT NULL,
 Fcontent TEXT NOT NULL,
 Fcreate_time DATETIME NOT NULL
);
CREATE INDEX idx_message_conv ON t_message(Fconv_id,Fid);
CREATE TABLE t_run (
 Fid INTEGER PRIMARY KEY AUTOINCREMENT,
 Frun_id TEXT NOT NULL UNIQUE,
 Fconv_id TEXT NOT NULL,
 Fuser_id TEXT NOT NULL,
 Frequest_id TEXT NOT NULL,
 Finput_hash TEXT NOT NULL,
 Fmodel TEXT NOT NULL,
 Fstatus INTEGER NOT NULL DEFAULT 0,
 Factive INTEGER DEFAULT 1,
 Fgeneration INTEGER NOT NULL DEFAULT 0,
 Fcancel_req INTEGER NOT NULL DEFAULT 0,
 Fstop_confirmed INTEGER NOT NULL DEFAULT 0,
 Fcheckpoint_status INTEGER NOT NULL DEFAULT 0,
 Fcommit TEXT NOT NULL DEFAULT '',
 Frt_seq_start INTEGER NOT NULL DEFAULT 0,
 Frt_seq_end INTEGER NOT NULL DEFAULT 0,
 Fiterations INTEGER NOT NULL DEFAULT 0,
 Fprompt_tokens INTEGER NOT NULL DEFAULT 0,
 Fcompl_tokens INTEGER NOT NULL DEFAULT 0,
 Ferror_code TEXT NOT NULL DEFAULT '',
 Ferror_msg TEXT NOT NULL DEFAULT '',
 Fstart_time DATETIME,
 Fend_time DATETIME,
 Fcreate_time DATETIME NOT NULL,
 Fmodify_time DATETIME NOT NULL,
 UNIQUE(Fconv_id,Frequest_id),
 UNIQUE(Fconv_id,Factive)
);
CREATE INDEX idx_run_active ON t_run(Factive,Fstatus);
CREATE INDEX idx_run_user_time ON t_run(Fuser_id,Fcreate_time);
CREATE TABLE t_event (
 Fid INTEGER PRIMARY KEY AUTOINCREMENT,
 Fconv_id TEXT NOT NULL,
 Fseq INTEGER NOT NULL,
 Frun_id TEXT NOT NULL DEFAULT '',
 Fsource INTEGER NOT NULL,
 Ftype TEXT NOT NULL,
 Fruntime_epoch TEXT,
 Fagent_session_id TEXT,
 Frt_seq INTEGER,
 Fpayload TEXT NOT NULL,
 Fcreate_time DATETIME NOT NULL,
 UNIQUE(Fconv_id,Fseq),
 UNIQUE(Fruntime_epoch,Fagent_session_id,Frt_seq)
);
CREATE TABLE t_sandbox (
 Fid INTEGER PRIMARY KEY AUTOINCREMENT,
 Fconv_id TEXT NOT NULL UNIQUE,
 Finst_id TEXT NOT NULL DEFAULT '',
 Fstatus INTEGER NOT NULL DEFAULT 0,
 Fcontainer_id TEXT NOT NULL DEFAULT '',
 Fvolume_name TEXT NOT NULL DEFAULT '',
 Fimage_ver TEXT NOT NULL DEFAULT '',
 Fnet_addr TEXT NOT NULL DEFAULT '',
 Ftoken_hash TEXT NOT NULL DEFAULT '',
 Ftoken_expire DATETIME,
 Fpreview_status INTEGER NOT NULL DEFAULT 0,
 Fpreview_gen INTEGER NOT NULL DEFAULT 0,
 Fpreview_commit TEXT NOT NULL DEFAULT '',
 Flast_active_time DATETIME NOT NULL,
 Fcreate_time DATETIME NOT NULL,
 Fmodify_time DATETIME NOT NULL
);
CREATE INDEX idx_sandbox_token_hash ON t_sandbox(Ftoken_hash);
CREATE INDEX idx_sandbox_status_active ON t_sandbox(Fstatus,Flast_active_time);
CREATE TABLE t_llm_usage (
 Fid INTEGER PRIMARY KEY AUTOINCREMENT,
 Fconv_id TEXT NOT NULL,
 Frun_id TEXT NOT NULL DEFAULT '',
 Fuser_id TEXT NOT NULL,
 Fmodel TEXT NOT NULL,
 Fprompt_tokens INTEGER NOT NULL DEFAULT 0,
 Fcompl_tokens INTEGER NOT NULL DEFAULT 0,
 Fcost_ms INTEGER NOT NULL DEFAULT 0,
 Fhttp_status INTEGER NOT NULL DEFAULT 0,
 Fcreate_time DATETIME NOT NULL
);
CREATE INDEX idx_llm_usage_conv_time ON t_llm_usage(Fconv_id,Fcreate_time);
CREATE INDEX idx_llm_usage_run ON t_llm_usage(Frun_id);
CREATE INDEX idx_llm_usage_user_time ON t_llm_usage(Fuser_id,Fcreate_time);

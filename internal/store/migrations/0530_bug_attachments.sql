CREATE TABLE bug_report_files (
    document_id bigint PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    report_id bigint NOT NULL REFERENCES bug_reports(id) ON DELETE CASCADE,
    name text NOT NULL
);
CREATE INDEX bug_report_files_report ON bug_report_files(report_id);

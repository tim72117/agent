-- Postgres schema for the analysis-mock backend (DB_DRIVER=postgres).
--
-- Mirrors the three things questions.go reads from SQL Server, with a
-- Postgres-native structure (see postgres.go for why this doesn't need to
-- match SQL Server's schema byte-for-byte — only the query *results* need
-- to match what questions.go expects):
--
--   1. file_analysis(id, tablename, file_id_ques)
--   2. file_ques_page(file_id, page, xml)
--   3. INFORMATION_SCHEMA.COLUMNS for the analysis target table, reached in
--      SQL Server via the cross-database "analysis_data.INFORMATION_SCHEMA
--      .COLUMNS" query. Postgres has no cross-database queries, so
--      analysis_columns is a same-database view postgres.go's query
--      rewriter redirects that lookup to.

CREATE TABLE IF NOT EXISTS file_analysis (
    id            SERIAL PRIMARY KEY,
    tablename     TEXT NOT NULL,
    file_id_ques  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS file_ques_page (
    file_id  INTEGER NOT NULL,
    page     INTEGER NOT NULL,
    xml      TEXT NOT NULL
);

-- analysis_columns stands in for SQL Server's
-- analysis_data.INFORMATION_SCHEMA.COLUMNS: one row per (table_name,
-- column_name) that questions.go treats as "this survey response table has
-- this column". In real SQL Server data this comes from the actual
-- analysis_data database's catalog; here it's just a plain table so sample
-- data can declare which columns exist without creating one real table per
-- survey.
CREATE TABLE IF NOT EXISTS analysis_columns (
    table_name   TEXT NOT NULL,
    column_name  TEXT NOT NULL
);

-- ---------------------------------------------------------------------------
-- Sample data: one survey ("demo_survey") with two census pages of XML
-- questions, matching the <question>/<question_sub> shape parsePageXML in
-- questions.go expects.
-- ---------------------------------------------------------------------------

INSERT INTO file_analysis (id, tablename, file_id_ques) VALUES
    (1, 'demo_survey', 100)
ON CONFLICT DO NOTHING;

INSERT INTO analysis_columns (table_name, column_name) VALUES
    ('demo_survey', 'q1'),
    ('demo_survey', 'q2')
ON CONFLICT DO NOTHING;

INSERT INTO file_ques_page (file_id, page, xml) VALUES
    (100, 1, '<page>
        <question>
            <id>1</id>
            <type>radio</type>
            <title>你最喜歡的顏色是？</title>
            <answer>
                <name>q1</name>
                <item value="1">紅色</item>
                <item value="2">藍色</item>
            </answer>
        </question>
        <question>
            <id>2</id>
            <type>radio</type>
            <title>你的滿意度？</title>
            <answer>
                <name>q2</name>
                <item value="1">滿意</item>
                <item value="2">普通</item>
                <item value="3">不滿意</item>
            </answer>
        </question>
    </page>')
ON CONFLICT DO NOTHING;

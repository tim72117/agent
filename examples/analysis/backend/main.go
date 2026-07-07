package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/microsoft/go-mssqldb"
)

var db *sql.DB

// .env (放在 mock 目錄下，自行填入):
//
//	DB_HOST=
//	DB_PORT=1433
//	DB_DATABASE=
//	DB_USERNAME=
//	DB_PASSWORD=
//	ANALYSIS_FILE_ID=        // file_analysis.id，用來抓 census pages 與 tablename
func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("[analysis-mock] 未載入 .env (%v)，改用環境變數", err)
	}

	var err error
	db, err = openDB()
	if err != nil {
		log.Fatalf("[analysis-mock] 連線資料庫失敗: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("[analysis-mock] 資料庫 Ping 失敗: %v", err)
	}
	log.Println("[analysis-mock] 資料庫已連線")

	http.HandleFunc("/analysis/", handleAnalysis)

	fmt.Println("[analysis-mock] listening on :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

// openDB picks the backing database from DB_DRIVER (default "sqlserver",
// so existing deployments keep working unchanged). "postgres" routes
// through openPostgresDB (postgres.go), which adapts questions.go's
// SQL-Server-flavored queries to run against Postgres without questions.go
// itself knowing the difference.
func openDB() (*sql.DB, error) {
	switch envOr("DB_DRIVER", "sqlserver") {
	case "postgres":
		dsn := buildPostgresDSN(
			os.Getenv("DB_HOST"),
			envOr("DB_PORT", "5432"),
			os.Getenv("DB_DATABASE"),
			os.Getenv("DB_USERNAME"),
			os.Getenv("DB_PASSWORD"),
		)
		return openPostgresDB(dsn)
	default:
		query := url.Values{}
		query.Add("database", os.Getenv("DB_DATABASE"))
		query.Add("encrypt", "disable")

		u := &url.URL{
			Scheme:   "sqlserver",
			User:     url.UserPassword(os.Getenv("DB_USERNAME"), os.Getenv("DB_PASSWORD")),
			Host:     hostPort(os.Getenv("DB_HOST"), envOr("DB_PORT", "1433")),
			RawQuery: query.Encode(),
		}
		return sql.Open("sqlserver", u.String())
	}
}

// hostPort 組出 URL 用的 host:port。
// SQL Server 慣用「主機,埠」(如 HOSTNAME,1433)；若 DB_HOST 已含
// 逗號或冒號指定埠，直接採用其埠，忽略 DB_PORT。
func hostPort(host, defaultPort string) string {
	if i := strings.IndexAny(host, ",:"); i >= 0 {
		return host[:i] + ":" + strings.TrimSpace(host[i+1:])
	}
	return host + ":" + defaultPort
}

func handleAnalysis(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("[mock] %s %s\n", r.Method, r.URL.Path)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/getAnalysisQuestions") {
		handleGetAnalysisQuestions(w, r)
		return
	}

	http.NotFound(w, r)
}

func handleGetAnalysisQuestions(w http.ResponseWriter, r *http.Request) {
	analysisID := envIntOr("ANALYSIS_FILE_ID", 0) // = file_analysis.id

	q := r.URL.Query()
	start := queryIntOr(q, "start", 0)
	amount := queryIntOr(q, "amount", -1) // -1 = 不限 (對應 PHP 預設 count)

	// 對應 PHP Session('analysis-columns-choosed')，mock 由 query 傳入 (逗號分隔)
	var chooseds []string
	if c := q.Get("choosed"); c != "" {
		chooseds = strings.Split(c, ",")
	}

	questions, err := getAnalysisQuestions(db, analysisID, chooseds, start, amount)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"questions": questions})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func queryIntOr(q url.Values, key string, def int) int {
	if v := q.Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
)

const defaultIndicatorID = "57824"

var defaultFilters = map[string][]string{
	"Классификатор видов экономической деятельности (ОКВЭД2)": {"*"},
	"Классификатор объектов административно-территориального деления (ОКАТО)": {"*"},
	"Период": {"январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"},
	"Год":   {"2022", "2023"},
}

var monthToNum = map[string]string{
	"январь":   "01",
	"февраль":  "02",
	"март":     "03",
	"апрель":   "04",
	"май":      "05",
	"июнь":     "06",
	"июль":     "07",
	"август":   "08",
	"сентябрь": "09",
	"октябрь":  "10",
	"ноябрь":   "11",
	"декабрь":  "12",
}

type dataIDRow struct {
	FieldID    string
	FieldTitle string
	ValueID    string
	ValueTitle string
	ObjectIDs  string
}

func strNorm(s string) string {
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.ToLower(s)
}

var scriptRe = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)

func extractScripts(html string) []string {
	m := scriptRe.FindAllStringSubmatch(html, -1)
	out := make([]string, 0, len(m))
	for _, mm := range m {
		if len(mm) > 1 {
			out = append(out, mm[1])
		}
	}
	return out
}

func isJSWordRune(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r > 127
}

func jsObjectLiteralToJSON(s string) string {
	// Go regexp does not support lookaheads, so we reproduce the original
	// boundary-quoting behavior with a small state machine.
	runes := []rune(s)
	var b strings.Builder
	inSingleQuotes := false

	for i, r := range runes {
		var prev rune
		if i > 0 {
			prev = runes[i-1]
		}
		if !inSingleQuotes && isJSWordRune(prev) != isJSWordRune(r) {
			b.WriteRune('\'')
		}

		b.WriteRune(r)
		if r == '\'' {
			inSingleQuotes = !inSingleQuotes
		}
	}

	if !inSingleQuotes && len(runes) > 0 && isJSWordRune(runes[len(runes)-1]) {
		b.WriteRune('\'')
	}

	return strings.ReplaceAll(b.String(), `'`, `"`)
}

func parseJS1(lines []string) (map[string]any, error) {
	start, end := -1, -1
	for i, line := range lines {
		if start == -1 && strings.Contains(line, "filters: {") {
			start = i + 1
		}
		if strings.Contains(line, "left_columns: [") {
			end = i - 2
			break
		}
	}
	if start == -1 || end == -1 || end < start {
		return nil, errors.New("не удалось найти блок filters/left_columns в JS")
	}
	frag := strings.Join(lines[start:end+1], "\n")
	asJSON := "{" + jsObjectLiteralToJSON(frag) + "}"
	var out map[string]any
	if err := json.Unmarshal([]byte(asJSON), &out); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JS filters в JSON: %w", err)
	}
	return out, nil
}

func parseJS2(lines []string) (map[string]any, error) {
	start, end := -1, -1
	leftColRe := regexp.MustCompile(`left_columns:\s*\[`)
	gridInitRe := regexp.MustCompile(`grid\.init\(\);\s*`)
	for i, line := range lines {
		if start == -1 && leftColRe.MatchString(line) {
			start = i
		}
		if start != -1 && gridInitRe.MatchString(line) {
			end = i - 2
			break
		}
	}
	if start == -1 || end == -1 || end < start {
		return nil, errors.New("не удалось найти блок left_columns/grid.init() в JS")
	}
	frag := strings.Join(lines[start:end+1], "\n")
	asJSON := "{" + jsObjectLiteralToJSON(frag) + "}"
	var parsed map[string]any
	if err := json.Unmarshal([]byte(asJSON), &parsed); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JS objectIds в JSON: %w", err)
	}

	rename := map[string]string{
		"left_columns":    "lineObjectIds",
		"top_columns":     "columnObjectIds",
		"groups":          "lineObjectIds",
		"filterObjectIds": "lineObjectIds",
	}
	out := map[string]any{}
	for k, v := range parsed {
		if nk, ok := rename[k]; ok {
			out[nk] = v
		} else {
			out[k] = v
		}
	}
	return out, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fedstatGetDataIDs(ctx context.Context, indicatorID string, client *http.Client) ([]dataIDRow, error) {
	u := fmt.Sprintf("https://www.fedstat.ru/indicator/%s", indicatorID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", getEnv("FEDSTAT_UA", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0 Safari/537.36"))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fedstat GET: %s: %s", resp.Status, string(b))
	}
	htmlBytes, _ := io.ReadAll(resp.Body)
	scripts := extractScripts(string(htmlBytes))
	var target string
	for _, s := range scripts {
		if strings.Contains(s, "filters:") && strings.Contains(s, "left_columns") {
			target = s
			break
		}
	}
	if target == "" {
		return nil, errors.New("не найден JS со списком фильтров (filters/left_columns)")
	}
	lines := strings.Split(target, "\n")
	filterList, err := parseJS1(lines)
	if err != nil {
		return nil, err
	}
	objectList, err := parseJS2(lines)
	if err != nil {
		return nil, err
	}

	// Ensure filterObjectIds contains "0" if missing
	containsZero := false
	for _, v := range objectList {
		switch vv := v.(type) {
		case []any:
			for _, x := range vv {
				if fmt.Sprint(x) == "0" {
					containsZero = true
				}
			}
		default:
			if fmt.Sprint(v) == "0" {
				containsZero = true
			}
		}
	}
	if !containsZero {
		objectList["filterObjectIds"] = []any{"0"}
	}

	fieldToObj := map[string]string{}
	for objName, v := range objectList {
		switch vv := v.(type) {
		case []any:
			for _, x := range vv {
				fid := fmt.Sprint(x)
				if _, ok := fieldToObj[fid]; !ok {
					fieldToObj[fid] = objName
				}
			}
		default:
			fid := fmt.Sprint(v)
			if _, ok := fieldToObj[fid]; !ok {
				fieldToObj[fid] = objName
			}
		}
	}

	var rows []dataIDRow
	for fieldID, objAny := range filterList {
		obj, ok := objAny.(map[string]any)
		if !ok {
			continue
		}
		title := fmt.Sprint(obj["title"])
		valuesAny, _ := obj["values"].(map[string]any)
		if len(valuesAny) == 0 {
			return nil, fmt.Errorf("fedstat вернул пустые значения для фильтра %q", title)
		}
		for valueID, valueObjAny := range valuesAny {
			valueObj, _ := valueObjAny.(map[string]any)
			vtitle := fmt.Sprint(valueObj["title"])
			vtitle = strings.ReplaceAll(vtitle, "&quot;", `"`)
			rows = append(rows, dataIDRow{
				FieldID:    fieldID,
				FieldTitle: title,
				ValueID:    valueID,
				ValueTitle: vtitle,
				ObjectIDs:  fieldToObj[fieldID],
			})
		}
	}

	if !hasIndicatorRow(rows) {
		return nil, errors.New("не найдена строка показателя (filter_field_id == 0)")
	}

	for i := range rows {
		if rows[i].ObjectIDs == "" {
			rows[i].ObjectIDs = "lineObjectIds"
		}
	}
	return rows, nil
}

func hasIndicatorRow(rows []dataIDRow) bool {
	for _, r := range rows {
		if r.FieldID == "0" {
			return true
		}
	}
	return false
}

func fedstatDataIDsFilter(dataIDs []dataIDRow, filters map[string][]string) ([]dataIDRow, error) {
	indicatorTitle := ""
	for _, r := range dataIDs {
		if r.FieldID == "0" {
			indicatorTitle = r.ValueTitle
			break
		}
	}
	if indicatorTitle == "" {
		return nil, errors.New("нет indicator title (field_id == 0)")
	}

	filtersAdded := map[string][]string{}
	for k, v := range filters {
		filtersAdded[k] = append([]string{}, v...)
	}
	filtersAdded["Показатель"] = []string{indicatorTitle}

	fieldTitleToID := map[string]string{}
	for _, r := range dataIDs {
		k := strNorm(r.FieldTitle)
		if _, ok := fieldTitleToID[k]; !ok {
			fieldTitleToID[k] = r.FieldID
		}
	}

	allowed := map[string]map[string]bool{} // nil map = wildcard
	wildcard := map[string]bool(nil)

	for fTitle, vals := range filtersAdded {
		fid, ok := fieldTitleToID[strNorm(fTitle)]
		if !ok {
			return nil, fmt.Errorf("фильтр не существует: %s", fTitle)
		}
		if len(vals) == 1 && vals[0] == "*" {
			allowed[fid] = wildcard
			continue
		}
		for _, v := range vals {
			if v == "*" {
				return nil, fmt.Errorf("поле %q содержит '*' вместе с другими значениями", fTitle)
			}
		}
		set := map[string]bool{}
		for _, v := range vals {
			set[strNorm(v)] = true
		}
		allowed[fid] = set
	}

	// Add unspecified as wildcard
	for _, r := range dataIDs {
		if _, ok := allowed[r.FieldID]; !ok {
			allowed[r.FieldID] = wildcard
		}
	}

	var out []dataIDRow
	for _, r := range dataIDs {
		a := allowed[r.FieldID]
		if a == nil || a[strNorm(r.ValueTitle)] {
			out = append(out, r)
		}
	}

	perField := map[string]int{}
	for _, r := range out {
		perField[r.FieldID]++
	}
	var missing []string
	for fid := range allowed {
		if perField[fid] == 0 {
			missing = append(missing, fid)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("нет значений для некоторых полей фильтра: %s", strings.Join(missing, ", "))
	}

	return out, nil
}

func fedstatPostDataIDsFiltered(ctx context.Context, client *http.Client, dataIDs []dataIDRow) ([]byte, error) {
	var indicator dataIDRow
	found := false
	for _, r := range dataIDs {
		if r.FieldID == "0" {
			indicator = r
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("нет indicator row (field_id == 0)")
	}

	postURL := "https://www.fedstat.ru/indicator/data.do?format=sdmx"

	seen := map[string]bool{}
	filtersUnique := make([]dataIDRow, 0)
	for _, r := range dataIDs {
		if seen[r.FieldID] {
			continue
		}
		seen[r.FieldID] = true
		filtersUnique = append(filtersUnique, r)
	}

	form := url.Values{}
	form.Set("format", "sdmx")
	form.Set("id", indicator.ValueID)
	form.Set("indicator_title", indicator.ValueTitle)

	// Duplicate keys must be preserved; url.Values doesn't preserve duplicates.
	// Build body manually in order.
	var bodyPairs [][2]string
	bodyPairs = append(bodyPairs, [2]string{"format", "sdmx"})
	bodyPairs = append(bodyPairs, [2]string{"id", indicator.ValueID})
	bodyPairs = append(bodyPairs, [2]string{"indicator_title", indicator.ValueTitle})

	for _, r := range filtersUnique {
		bodyPairs = append(bodyPairs, [2]string{r.ObjectIDs, r.FieldID})
	}
	for _, r := range dataIDs {
		bodyPairs = append(bodyPairs, [2]string{"selectedFilterIds", fmt.Sprintf("%s_%s", r.FieldID, r.ValueID)})
	}

	var buf strings.Builder
	for i, p := range bodyPairs {
		if i > 0 {
			buf.WriteByte('&')
		}
		buf.WriteString(url.QueryEscape(p[0]))
		buf.WriteByte('=')
		buf.WriteString(url.QueryEscape(p[1]))
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, postURL, strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", getEnv("FEDSTAT_UA", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0 Safari/537.36"))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fedstat POST: %s: %s", resp.Status, string(b))
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("content-type"), ";")[0]))
	if ct != "text/xml" && ct != "application/xml" {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("неожиданный content-type %q, body: %s", ct, string(b))
	}
	return io.ReadAll(resp.Body)
}

// Minimal SDMX parsing for the required columns, mirroring fedstatAPIr output shape:
// each dimension becomes a "title" column + "<dim>_code" column, plus ObsValue.

type xmlCodeList struct {
	XMLName xml.Name `xml:"CodeList"`
	ID      string   `xml:"id,attr"`
	Codes   []xmlCode `xml:"Code"`
}

type xmlCode struct {
	XMLName xml.Name `xml:"Code"`
	Value   string   `xml:"value,attr"`
	Title   string   `xml:",chardata"`
}

type anyXML struct {
	XMLName xml.Name
	Attr    []xml.Attr `xml:",any,attr"`
	Nodes   []anyXML   `xml:",any"`
	Text    string     `xml:",chardata"`
}

func findAll(n anyXML, name string, out *[]anyXML) {
	if n.XMLName.Local == name {
		*out = append(*out, n)
	}
	for _, ch := range n.Nodes {
		findAll(ch, name, out)
	}
}

func attr(n anyXML, key string) string {
	for _, a := range n.Attr {
		if a.Name.Local == key {
			return a.Value
		}
	}
	return ""
}

func fedstatParseSDMXToRows(data []byte) ([]map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false

	var root anyXML
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}

	var codeLists []anyXML
	findAll(root, "CodeList", &codeLists)
	codelistMap := map[string]map[string]string{}
	for _, cl := range codeLists {
		id := attr(cl, "id")
		if id == "" {
			continue
		}
		mp := map[string]string{}
		var codes []anyXML
		findAll(cl, "Code", &codes)
		for _, c := range codes {
			vid := attr(c, "value")
			title := strings.TrimSpace(c.Text)
			if vid != "" {
				mp[vid] = title
			}
		}
		codelistMap[id] = mp
	}

	var seriesNodes []anyXML
	findAll(root, "Series", &seriesNodes)
	rows := make([]map[string]string, 0, 1024)

	for _, s := range seriesNodes {
		dims := map[string]string{}

		var seriesKey []anyXML
		findAll(s, "SeriesKey", &seriesKey)
		if len(seriesKey) > 0 {
			var vals []anyXML
			findAll(seriesKey[0], "Value", &vals)
			for _, v := range vals {
				concept := attr(v, "concept")
				if concept == "" {
					concept = attr(v, "id")
				}
				value := attr(v, "value")
				if concept != "" && value != "" {
					dims[concept] = value
				}
			}
		}

		var obsNodes []anyXML
		findAll(s, "Obs", &obsNodes)
		for _, obs := range obsNodes {
			obsDims := map[string]string{}
			for k, v := range dims {
				obsDims[k] = v
			}

			var od []anyXML
			findAll(obs, "ObsDimension", &od)
			if len(od) > 0 {
				concept := attr(od[0], "concept")
				if concept == "" {
					concept = attr(od[0], "id")
				}
				if concept == "" {
					concept = "Time"
				}
				if concept == "TIME_PERIOD" {
					concept = "Time"
				}
				value := attr(od[0], "value")
				if value != "" {
					obsDims[concept] = value
				}
			}

			var ov []anyXML
			findAll(obs, "ObsValue", &ov)
			if len(ov) == 0 {
				continue
			}
			obsValue := attr(ov[0], "value")
			obsDims["ObsValue"] = obsValue

			out := map[string]string{}
			for k, code := range obsDims {
				if k == "ObsValue" {
					out[k] = code
					continue
				}
				out[k+"_code"] = code
				if mp, ok := codelistMap[k]; ok {
					if t, ok2 := mp[code]; ok2 {
						out[k] = t
					} else {
						out[k] = code
					}
				} else {
					out[k] = code
				}
			}
			rows = append(rows, out)
		}
	}

	return rows, nil
}

func decimalToSalaryStr(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.ReplaceAll(raw, ",", ".")
	// Keep as-is without scientific notation and then replace '.' -> ','
	// If parsing fails, fallback to raw replacement.
	if _, err := fmt.Sscanf(raw, "%f", new(float64)); err != nil {
		return strings.ReplaceAll(raw, ".", ",")
	}
	// Preserve raw textual form but normalize trailing zeros similar to Python impl
	// (R's as.numeric + printing is not strictly defined; this is the closest stable behavior).
	raw = strings.TrimRight(raw, "0")
	raw = strings.TrimRight(raw, ".")
	return strings.ReplaceAll(raw, ".", ",")
}

type outRow struct {
	Activity      string
	RegionName    string
	TimePeriod    string
	SalaryAverage string
	UpdateTime    string
}

func transformRowsLikeR(rows []map[string]string) ([]outRow, error) {
	// Drop columns (if present) and filter PERIOD with '-'
	filtered := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		delete(r, "EI")
		delete(r, "s_OKATO_code")
		delete(r, "s_OKVED2_code")
		if strings.Contains(r["PERIOD"], "-") {
			continue
		}
		if mn, ok := monthToNum[r["PERIOD"]]; ok {
			r["PERIOD"] = mn
		}
		r["PERIOD"] = r["Time"] + r["PERIOD"]
		delete(r, "Time")
		filtered = append(filtered, r)
	}

	now := time.Now().Format("2006-01-02 15:04:05")

	pick := func(r map[string]string, keys ...string) (string, bool) {
		for _, k := range keys {
			if _, ok := r[k]; ok {
				return k, true
			}
		}
		return "", false
	}

	out := make([]outRow, 0, len(filtered))
	for _, r := range filtered {
		kAct, ok1 := pick(r, "s_OKVED2", "OKVED2", "ОКВЭД2")
		kReg, ok2 := pick(r, "s_OKATO", "OKATO", "ОКАТО")
		if !ok1 || !ok2 {
			return nil, errors.New("не удалось определить колонки деятельности/региона (ожидались s_OKVED2/s_OKATO или аналоги)")
		}
		out = append(out, outRow{
			Activity:      r[kAct],
			RegionName:    r[kReg],
			TimePeriod:    r["PERIOD"],
			SalaryAverage: decimalToSalaryStr(r["ObsValue"]),
			UpdateTime:    now,
		})
	}
	return out, nil
}

func mssqlTruncateAndInsert(ctx context.Context, rows []outRow) error {
	if os.Getenv("SKIP_MSSQL") == "1" {
		return nil
	}

	server := getEnv("MSSQL_SERVER", "000.000.000.000")
	port := getEnv("MSSQL_PORT", "1433")
	user := getEnv("MSSQL_UID", "FedStat")
	pass := getEnv("MSSQL_PWD", "********")
	database := getEnv("MSSQL_DATABASE", "imports")
	table := getEnv("MSSQL_TABLE", "salary_stat_test")

	connURL := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(user, pass),
		Host:   fmt.Sprintf("%s:%s", server, port),
	}
	q := connURL.Query()
	q.Add("database", database)
	q.Add("encrypt", getEnv("MSSQL_ENCRYPT", "true"))
	q.Add("TrustServerCertificate", getEnv("MSSQL_TRUST_SERVER_CERT", "true"))
	connURL.RawQuery = q.Encode()

	db, err := sql.Open("sqlserver", connURL.String())
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+table+";"); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+table+" (activity, region_name, time_period, salary_average, update_time) VALUES (@p1,@p2,@p3,@p4,@p5)")
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	limit := len(rows)
	if limit > 200000 {
		limit = 200000
	}
	for i := 0; i < limit; i++ {
		r := rows[i]
		if _, err := stmt.ExecContext(ctx, r.Activity, r.RegionName, r.TimePeriod, r.SalaryAverage, r.UpdateTime); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func writeCSV(rows []outRow) error {
	if os.Getenv("SKIP_CSV") == "1" {
		return nil
	}
	csvPath := getEnv("CSV_PATH", `C:\Users\vovaadmin\Documents\ПРОЕКТЫ\СКИТ.ПАСПОРТИЗАЦИЯ\Зарплаты\data_2023-08-28_12-19.csv`)
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(csvPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	_ = w.Write([]string{"activity", "region_name", "time_period", "salary_average", "update_time"})
	for _, r := range rows {
		_ = w.Write([]string{r.Activity, r.RegionName, r.TimePeriod, r.SalaryAverage, r.UpdateTime})
	}
	return w.Error()
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	indicatorID := getEnv("INDICATOR_ID", defaultIndicatorID)
	client := &http.Client{Timeout: 180 * time.Second}

	dataIDs, err := fedstatGetDataIDs(ctx, indicatorID, client)
	if err != nil {
		panic(err)
	}
	filteredIDs, err := fedstatDataIDsFilter(dataIDs, defaultFilters)
	if err != nil {
		panic(err)
	}
	sdmx, err := fedstatPostDataIDsFiltered(ctx, client, filteredIDs)
	if err != nil {
		panic(err)
	}
	rows, err := fedstatParseSDMXToRows(sdmx)
	if err != nil {
		panic(err)
	}
	out, err := transformRowsLikeR(rows)
	if err != nil {
		panic(err)
	}

	if err := mssqlTruncateAndInsert(ctx, out); err != nil {
		panic(err)
	}
	if err := writeCSV(out); err != nil {
		panic(err)
	}

	fmt.Printf("OK: rows=%d\n", len(out))
}


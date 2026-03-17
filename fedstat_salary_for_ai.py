import os
import re
import sys
import json
import csv
import time
import decimal
import datetime as dt
from dataclasses import dataclass
from typing import Dict, Iterable, List, Optional, Sequence, Tuple

import requests


DEFAULT_INDICATOR_ID = "57824"

DEFAULT_FILTERS = {
    "Классификатор видов экономической деятельности (ОКВЭД2)": ["*"],
    "Классификатор объектов административно-территориального деления (ОКАТО)": ["*"],
    "Период": [
        "январь",
        "февраль",
        "март",
        "апрель",
        "май",
        "июнь",
        "июль",
        "август",
        "сентябрь",
        "октябрь",
        "ноябрь",
        "декабрь",
    ],
    "Год": ["2022", "2023"],
}

MONTH_TO_NUM = {
    "январь": "01",
    "февраль": "02",
    "март": "03",
    "апрель": "04",
    "май": "05",
    "июнь": "06",
    "июль": "07",
    "август": "08",
    "сентябрь": "09",
    "октябрь": "10",
    "ноябрь": "11",
    "декабрь": "12",
}


@dataclass(frozen=True)
class DataIdRow:
    filter_field_id: str
    filter_field_title: str
    filter_value_id: str
    filter_value_title: str
    filter_field_object_ids: str


def _str_norm(s: str) -> str:
    return re.sub(r"\s+", " ", s.strip()).lower()


def _extract_scripts(html: str) -> List[str]:
    # Minimal, dependency-free script extractor.
    return re.findall(r"<script\b[^>]*>(.*?)</script>", html, flags=re.IGNORECASE | re.DOTALL)


_WORD_BOUNDARY_OUTSIDE_SINGLE_QUOTES = re.compile(r"\b(?=([^']*'[^']*')*[^']*$)")


def _js_object_literal_to_json(text: str) -> str:
    # Mirrors fedstatAPIr approach:
    # 1) Insert single quotes on word boundaries outside of existing single-quoted strings
    # 2) Convert single quotes to double quotes
    # This tends to turn JS object literal fragments into JSON-ish text.
    text = _WORD_BOUNDARY_OUTSIDE_SINGLE_QUOTES.sub("'", text)
    text = text.replace("'", '"')
    return text


def _parse_js1(lines: Sequence[str]) -> dict:
    start = None
    end = None
    for i, line in enumerate(lines):
        if start is None and "filters: {" in line:
            start = i + 1
        if "left_columns: [" in line:
            end = i - 2
            break
    if start is None or end is None or end < start:
        raise RuntimeError("Не удалось найти блок filters/left_columns в JS на странице fedstat.")

    fragment = "\n".join(lines[start : end + 1])
    as_json = "{" + _js_object_literal_to_json(fragment) + "}"
    return json.loads(as_json)


def _parse_js2(lines: Sequence[str]) -> dict:
    start = None
    end = None
    for i, line in enumerate(lines):
        if start is None and re.search(r"left_columns:\s*\[", line):
            start = i
        if start is not None and re.search(r"grid\.init\(\);\s*", line):
            end = i - 2
            break
    if start is None or end is None or end < start:
        raise RuntimeError("Не удалось найти блок left_columns/grid.init() в JS на странице fedstat.")

    fragment = "\n".join(lines[start : end + 1])
    as_json = "{" + _js_object_literal_to_json(fragment) + "}"
    parsed = json.loads(as_json)

    rename = {
        "left_columns": "lineObjectIds",
        "top_columns": "columnObjectIds",
        "groups": "lineObjectIds",
        "filterObjectIds": "lineObjectIds",
    }
    out = {}
    for k, v in parsed.items():
        out[rename.get(k, k)] = v
    return out


def fedstat_get_data_ids(indicator_id: str, session: requests.Session) -> List[DataIdRow]:
    url = f"https://www.fedstat.ru/indicator/{indicator_id}"
    headers = {
        # fedstat may block default client headers; use a browser-like UA.
        "User-Agent": os.environ.get(
            "FEDSTAT_UA",
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0 Safari/537.36",
        )
    }
    res = session.get(url, headers=headers, timeout=180)
    res.raise_for_status()

    scripts = _extract_scripts(res.text)
    target = None
    for s in scripts:
        if "filters:" in s and "left_columns" in s:
            target = s
            break
    if target is None:
        raise RuntimeError("Не найден JS со списком фильтров (filters/left_columns) на странице индикатора.")

    lines = target.splitlines()
    filter_list = _parse_js1(lines)
    object_list = _parse_js2(lines)

    # Ensure filterObjectIds contains indicator id as "0" if missing
    flattened_vals = []
    for vals in object_list.values():
        if isinstance(vals, list):
            flattened_vals.extend([str(x) for x in vals])
        else:
            flattened_vals.append(str(vals))
    if all(v != "0" for v in flattened_vals):
        object_list["filterObjectIds"] = ["0"]

    # Build mapping filter_field_id -> filter_field_object_ids (lineObjectIds/columnObjectIds/filterObjectIds)
    field_id_to_object_ids: Dict[str, str] = {}
    for obj_name, vals in object_list.items():
        if not isinstance(vals, list):
            vals = [vals]
        for v in vals:
            field_id_to_object_ids.setdefault(str(v), obj_name)

    rows: List[DataIdRow] = []
    for filter_field_id, obj in filter_list.items():
        title = str(obj.get("title", ""))
        values = obj.get("values", {}) or {}
        if not isinstance(values, dict) or not values:
            raise RuntimeError(f'fedstat вернул пустые значения для фильтра "{title}"')
        for value_id, value_obj in values.items():
            vtitle = str((value_obj or {}).get("title", ""))
            vtitle = vtitle.replace("&quot;", '"')
            rows.append(
                DataIdRow(
                    filter_field_id=str(filter_field_id),
                    filter_field_title=title,
                    filter_value_id=str(value_id),
                    filter_value_title=vtitle,
                    filter_field_object_ids=field_id_to_object_ids.get(str(filter_field_id), "lineObjectIds"),
                )
            )

    # Make sure indicator row "0" exists
    if not any(r.filter_field_id == "0" for r in rows):
        raise RuntimeError("Не найдена строка показателя (filter_field_id == 0).")

    return rows


def fedstat_data_ids_filter(data_ids: List[DataIdRow], filters: Dict[str, List[str]]) -> List[DataIdRow]:
    # Add indicator title filter ("Показатель") like fedstatAPIr does.
    indicator_title = next(r.filter_value_title for r in data_ids if r.filter_field_id == "0")
    filters_added = dict(filters)
    filters_added["Показатель"] = [indicator_title]

    # Normalize
    data_ids_norm = [
        (
            r,
            _str_norm(r.filter_field_title),
            _str_norm(r.filter_value_title),
        )
        for r in data_ids
    ]

    # Map normalized field_title -> field_id
    field_title_norm_to_id: Dict[str, str] = {}
    for r, f_norm, _ in data_ids_norm:
        field_title_norm_to_id.setdefault(f_norm, r.filter_field_id)

    # Build per-field allowed normalized values set (or wildcard)
    allowed: Dict[str, Optional[set]] = {}
    for field_title, values in filters_added.items():
        f_norm = _str_norm(field_title)
        field_id = field_title_norm_to_id.get(f_norm)
        if field_id is None:
            raise RuntimeError(f"Фильтр назван неверно или не существует: {field_title}")
        vals = [str(v) for v in values]
        if any(v == "*" for v in vals):
            if len(vals) != 1:
                raise RuntimeError(f'Поле "{field_title}" содержит "*" вместе с другими значениями.')
            allowed[field_id] = None  # wildcard
        else:
            allowed[field_id] = {_str_norm(v) for v in vals}

    # Add unspecified fields as wildcard (select all possible)
    for r in data_ids:
        allowed.setdefault(r.filter_field_id, None)

    filtered: List[DataIdRow] = []
    for r, _, v_norm in data_ids_norm:
        a = allowed.get(r.filter_field_id)
        if a is None or v_norm in a:
            filtered.append(r)

    # Basic sanity: at least one value per field
    per_field_counts: Dict[str, int] = {}
    for r in filtered:
        per_field_counts[r.filter_field_id] = per_field_counts.get(r.filter_field_id, 0) + 1
    missing = [fid for fid in allowed.keys() if per_field_counts.get(fid, 0) == 0]
    if missing:
        raise RuntimeError(f"Нет значений для некоторых полей фильтра: {', '.join(missing)}")

    return filtered


def fedstat_post_data_ids_filtered(session: requests.Session, data_ids: List[DataIdRow]) -> bytes:
    indicator = next(r for r in data_ids if r.filter_field_id == "0")
    data_format = "sdmx"
    post_url = f"https://www.fedstat.ru/indicator/data.do?format={data_format}"

    # unique filter fields with their object ids (lineObjectIds/columnObjectIds/filterObjectIds)
    seen_fields = set()
    filters_unique: List[Tuple[str, str]] = []
    for r in data_ids:
        if r.filter_field_id in seen_fields:
            continue
        seen_fields.add(r.filter_field_id)
        filters_unique.append((r.filter_field_object_ids, r.filter_field_id))

    # selectedFilterIds repeats for each chosen filter value
    selected: List[Tuple[str, str]] = [("selectedFilterIds", f"{r.filter_field_id}_{r.filter_value_id}") for r in data_ids]

    body: List[Tuple[str, str]] = [
        ("format", data_format),
        ("id", indicator.filter_value_id),
        ("indicator_title", indicator.filter_value_title),
    ]
    body.extend(filters_unique)
    body.extend(selected)

    headers = {
        "User-Agent": os.environ.get(
            "FEDSTAT_UA",
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0 Safari/537.36",
        )
    }
    res = session.post(post_url, data=body, headers=headers, timeout=180)
    res.raise_for_status()
    ct = (res.headers.get("content-type") or "").split(";")[0].strip().lower()
    if ct not in ("text/xml", "application/xml"):
        raise RuntimeError(f"Неожиданный content-type от fedstat: {ct!r} (возможно, нет данных или сайт лагает)")
    return res.content


def _strip_namespaces_inplace(elem) -> None:
    for el in elem.iter():
        if "}" in el.tag:
            el.tag = el.tag.split("}", 1)[1]


def fedstat_parse_sdmx_to_table(data_raw: bytes) -> List[Dict[str, str]]:
    import xml.etree.ElementTree as ET

    root = ET.fromstring(data_raw)
    _strip_namespaces_inplace(root)

    # Build codelist map: field_id -> {value_id -> value_title}
    codelists: Dict[str, Dict[str, str]] = {}
    for cl in root.findall(".//CodeList"):
        field_id = cl.attrib.get("id")
        if not field_id:
            continue
        mp: Dict[str, str] = {}
        for code in cl.findall(".//Code"):
            vid = code.attrib.get("value")
            vtitle = (code.text or "").strip()
            if vid is not None:
                mp[str(vid)] = vtitle
        codelists[field_id] = mp

    rows: List[Dict[str, str]] = []
    for series in root.findall(".//Series"):
        dims: Dict[str, str] = {}
        for v in series.findall("./SeriesKey/Value"):
            concept = v.attrib.get("concept") or v.attrib.get("id")
            value = v.attrib.get("value")
            if concept and value is not None:
                dims[concept] = str(value)

        for obs in series.findall("./Obs"):
            obs_dims = dict(dims)
            od = obs.find("./ObsDimension")
            if od is not None:
                concept = od.attrib.get("concept") or od.attrib.get("id") or "Time"
                value = od.attrib.get("value")
                if concept == "TIME_PERIOD":
                    concept = "Time"
                if value is not None:
                    obs_dims[concept] = str(value)

            ov = obs.find("./ObsValue")
            if ov is None:
                continue
            obs_value = ov.attrib.get("value")
            obs_dims["ObsValue"] = "" if obs_value is None else str(obs_value)

            # Expand to "title" + "_code" columns like fedstatAPIr does
            out: Dict[str, str] = {}
            for k, code in obs_dims.items():
                if k == "ObsValue":
                    out[k] = code
                    continue
                out[f"{k}_code"] = code
                out[k] = codelists.get(k, {}).get(code, code)
            rows.append(out)

    return rows


def _decimal_to_salary_str(value_raw: str) -> str:
    # fedstatAPIr parses ObsValue via as.numeric(gsub(",", ".", ObsValue)),
    # then script replaces "." -> ",". We'll keep decimal precision as-is.
    s = (value_raw or "").strip()
    if s == "":
        return ""
    s = s.replace(",", ".")
    d = decimal.Decimal(s)
    # Avoid scientific notation; normalize but keep trailing zeros if provided by source is not guaranteed.
    out = format(d, "f").rstrip("0").rstrip(".") if "." in format(d, "f") else format(d, "f")
    return out.replace(".", ",")


def transform_rows_like_r(rows: List[Dict[str, str]]) -> List[Dict[str, str]]:
    # Drop columns as in R script (if present)
    for r in rows:
        r.pop("EI", None)
        r.pop("s_OKATO_code", None)
        r.pop("s_OKVED2_code", None)

    # Remove rows where PERIOD contains "-"
    rows = [r for r in rows if "-" not in str(r.get("PERIOD", ""))]

    # Replace month names with numbers
    for r in rows:
        p = str(r.get("PERIOD", ""))
        if p in MONTH_TO_NUM:
            r["PERIOD"] = MONTH_TO_NUM[p]

    # PERIOD <- paste0(Time, PERIOD); remove Time
    for r in rows:
        r["PERIOD"] = f"{r.get('Time','')}{r.get('PERIOD','')}"
        r.pop("Time", None)

    # update_time formatted for MS SQL Server
    now = dt.datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    for r in rows:
        r["update_time"] = now

    # Map to DB column names and order like R
    def pick_key(d: Dict[str, str], candidates: Sequence[str]) -> Optional[str]:
        for c in candidates:
            if c in d:
                return c
        return None

    out_rows: List[Dict[str, str]] = []
    for r in rows:
        k_activity = pick_key(r, ["s_OKVED2", "OKVED2", "ОКВЭД2"])
        k_region = pick_key(r, ["s_OKATO", "OKATO", "ОКАТО"])
        if k_activity is None or k_region is None:
            raise RuntimeError(
                "Не удалось определить колонки деятельности/региона (ожидались s_OKVED2/s_OKATO или аналоги)."
            )
        salary_raw = r.get("ObsValue", "")
        out_rows.append(
            {
                "activity": str(r.get(k_activity, "")),
                "region_name": str(r.get(k_region, "")),
                "time_period": str(r.get("PERIOD", "")),
                "salary_average": _decimal_to_salary_str(str(salary_raw)),
                "update_time": now,
            }
        )
    return out_rows


def mssql_truncate_and_insert(rows: List[Dict[str, str]]) -> None:
    import pyodbc

    driver = os.environ.get("MSSQL_DRIVER", "ODBC Driver 18 for SQL Server")
    server = os.environ.get("MSSQL_SERVER", "000.000.000.000")
    database = os.environ.get("MSSQL_DATABASE", "imports")
    uid = os.environ.get("MSSQL_UID", "FedStat")
    pwd = os.environ.get("MSSQL_PWD", "********")
    table = os.environ.get("MSSQL_TABLE", "salary_stat_test")

    encrypt = os.environ.get("MSSQL_ENCRYPT", "yes")
    trust = os.environ.get("MSSQL_TRUST_SERVER_CERT", "yes")
    port = os.environ.get("MSSQL_PORT", "1433")

    conn_str = (
        f"DRIVER={{{driver}}};"
        f"SERVER={server},{port};"
        f"DATABASE={database};"
        f"UID={uid};PWD={pwd};"
        f"Encrypt={encrypt};TrustServerCertificate={trust};"
    )

    with pyodbc.connect(conn_str) as con:
        cur = con.cursor()
        cur.execute(f"TRUNCATE TABLE {table};")

        cur.fast_executemany = True
        sql = f"INSERT INTO {table} (activity, region_name, time_period, salary_average, update_time) VALUES (?,?,?,?,?)"
        params = [
            (r["activity"], r["region_name"], r["time_period"], r["salary_average"], r["update_time"])
            for r in rows[:200000]
        ]
        cur.executemany(sql, params)
        con.commit()


def write_csv(rows: List[Dict[str, str]]) -> None:
    csv_path = os.environ.get(
        "CSV_PATH",
        r"C:\Users\vovaadmin\Documents\ПРОЕКТЫ\СКИТ.ПАСПОРТИЗАЦИЯ\Зарплаты\data_2023-08-28_12-19.csv",
    )
    os.makedirs(os.path.dirname(csv_path), exist_ok=True)
    with open(csv_path, "w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=["activity", "region_name", "time_period", "salary_average", "update_time"])
        w.writeheader()
        for r in rows:
            w.writerow(r)


def main() -> int:
    indicator_id = os.environ.get("INDICATOR_ID", DEFAULT_INDICATOR_ID)
    session = requests.Session()

    data_ids = fedstat_get_data_ids(indicator_id, session)
    filtered_ids = fedstat_data_ids_filter(data_ids, DEFAULT_FILTERS)
    sdmx_raw = fedstat_post_data_ids_filtered(session, filtered_ids)
    table = fedstat_parse_sdmx_to_table(sdmx_raw)
    out_rows = transform_rows_like_r(table)

    if os.environ.get("SKIP_MSSQL") != "1":
        mssql_truncate_and_insert(out_rows)

    if os.environ.get("SKIP_CSV") != "1":
        write_csv(out_rows)

    print(f"OK: rows={len(out_rows)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())


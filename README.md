## Что сделано

Этот проект переписывает функционал `fedstat_salary_for_ai.r` **1:1** на:

- **Python**: `fedstat_salary_for_ai.py`
- **Go**: `main.go`

Обе версии:

- Загружают данные с `fedstat.ru` по индикатору **57824** (через разбор JS на странице индикатора → получение внутренних id фильтров → POST `data.do?format=sdmx`).
- Делают те же преобразования, что и R:
  - удаляют `EI`, `s_OKATO_code`, `s_OKVED2_code` (если они присутствуют),
  - удаляют строки с `'-'` в `PERIOD`,
  - заменяют месяцы `январь..декабрь` на `01..12`,
  - склеивают `time_period = Time + PERIOD` (например `2022` + `01` → `202201`) и удаляют `Time`,
  - добавляют `update_time` в формате `YYYY-MM-DD HH:MM:SS`,
  - приводят `salary_average` к строке с десятичной **запятой** (замена `.` → `,`).
- Подключаются к MS SQL Server, выполняют `TRUNCATE TABLE salary_stat_test;`, затем вставляют до **200000** строк.
- Пишут CSV (путь по умолчанию — как в R-скрипте).

## Настройки (через переменные окружения)

Общие:

- **`INDICATOR_ID`**: по умолчанию `57824`
- **`FEDSTAT_UA`**: User-Agent (полезно при 403), по умолчанию “как браузер”
- **`CSV_PATH`**: путь CSV; по умолчанию как в R:
  - `C:\Users\vovaadmin\Documents\ПРОЕКТЫ\СКИТ.ПАСПОРТИЗАЦИЯ\Зарплаты\data_2023-08-28_12-19.csv`
- **`SKIP_MSSQL`**: `1` чтобы не грузить в БД
- **`SKIP_CSV`**: `1` чтобы не писать CSV

MS SQL (Python и Go):

- **`MSSQL_SERVER`**
- **`MSSQL_PORT`** (по умолчанию `1433`)
- **`MSSQL_DATABASE`** (по умолчанию `imports`)
- **`MSSQL_UID`** (по умолчанию `FedStat`)
- **`MSSQL_PWD`**
- **`MSSQL_TABLE`** (по умолчанию `salary_stat_test`)
- **`MSSQL_ENCRYPT`**, **`MSSQL_TRUST_SERVER_CERT`**

Дополнительно для Python (ODBC):

- **`MSSQL_DRIVER`** (по умолчанию `ODBC Driver 18 for SQL Server`)

## Запуск Python

Установить зависимости:

```bash
python -m pip install -r python_requirements.txt
```

Запуск:

```bash
python fedstat_salary_for_ai.py
```

## Запуск Go

Сборка:

```bash
go build
```

Запуск:

```bash
./fedstat_salary_for_ai.exe
```


#!/usr/bin/env r

# Порядок установки окружения скрипта:

# На Linux REDOS 7.3 предварительно выполнить следующие команды из командной строки суперпользователя:
# yum install R-littler
# yum install R-devel
# yum install libcurl-devel
# yum install openssl-devel
# yum install harfbuzz-devel
# yum install fribidi-devel
# yum install freetype-devel libpng-devel libtiff-devel libjpeg-turbo-devel # нужен для install.packages("stringr")
# yum install unixODBC-devel # нужен для install.packages("odbc")

# После этого зайти в интерпретатор R и выполнить следующие команды в интерактивном режиме R
# > install.packages("fedstatAPIr")
# > install.packages("devtools")
# > install.packages("odbc")

# > install.packages("stringr") # нужен для devtools::install_github("bescoto/RMSSQL")
# Необходимые пакеты для stringr 
# https://cran.rstudio.com/bin/windows/Rtools/
# устанавливаю также зависимости ‘cli’, ‘glue’, ‘lifecycle’, ‘rlang’, ‘stringi’, ‘vctrs’
# 'https://cran.rstudio.com/bin/windows/contrib/4.3/cli_3.6.1.zip'
# 'https://cran.rstudio.com/bin/windows/contrib/4.3/glue_1.6.2.zip'
# 'https://cran.rstudio.com/bin/windows/contrib/4.3/lifecycle_1.0.3.zip'
# 'https://cran.rstudio.com/bin/windows/contrib/4.3/rlang_1.1.1.zip'
# 'https://cran.rstudio.com/bin/windows/contrib/4.3/stringi_1.7.12.zip'
# 'https://cran.rstudio.com/bin/windows/contrib/4.3/vctrs_0.6.3.zip'
# 'https://cran.rstudio.com/bin/windows/contrib/4.3/stringr_1.5.0.zip'

# > devtools::install_github("bescoto/RMSSQL")
# > выход из R в интерактивном режиме: q()

# Нас интересует показатель, который на сайте ЕМИСС называется: 
# "Среднемесячная номинальная начисленная заработная плата работающих в экономике с 2017 г."

# Виды деятельности: 
# - "Деятельность по чистке и уборке"
# - "Деятельность административно-хозяйственная, вспомогательная деятельность по 
# обеспечению функционирования организации, деятельность по предоставлению 
# прочих вспомогательных услуг для бизнеса"

library(fedstatAPIr)
data <- fedstat_data_load_with_filters(
  indicator_id = "57824",
  filters = list(
    "Классификатор видов экономической деятельности (ОКВЭД2)" = "*",
	"Классификатор объектов административно-территориального деления (ОКАТО)" = "*",    
    # "Период" = "*",
	"Период" = c("январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"),
    "Год" = c("2022", "2023")
  )
)

# удаляем ненужные столбцы из таблицы данных
data$EI <- NULL
data$s_OKATO_code <- NULL
data$s_OKVED2_code <- NULL

# Удаляем строки, содержащие '-' в поле PERIOD, например, 'январь-февраль' и т.п.
data = data[!grepl('-', data$PERIOD),]

# Подключаем библиотеку для замены строк
library (stringr)

# Меняем "январь" на 01, "февраль" на 02 и т.д.
data$PERIOD <- str_replace(data$PERIOD, "январь", "01")
data$PERIOD <- str_replace(data$PERIOD, "февраль", "02")
data$PERIOD <- str_replace(data$PERIOD, "март", "03")
data$PERIOD <- str_replace(data$PERIOD, "апрель", "04")
data$PERIOD <- str_replace(data$PERIOD, "май", "05")
data$PERIOD <- str_replace(data$PERIOD, "июнь", "06")
data$PERIOD <- str_replace(data$PERIOD, "июль", "07")
data$PERIOD <- str_replace(data$PERIOD, "август", "08")
data$PERIOD <- str_replace(data$PERIOD, "сентябрь", "09")
data$PERIOD <- str_replace(data$PERIOD, "октябрь", "10")
data$PERIOD <- str_replace(data$PERIOD, "ноябрь", "11")
data$PERIOD <- str_replace(data$PERIOD, "декабрь", "12")

# Приклеиваем номер месяца справа от номера года. 
# Например, если PERIOD = '01', Time = '2022' => PERIOD = "202201"
data$PERIOD <- paste0(data$Time, data$PERIOD)

# Год теперь не нужен. Удаляем колонку
data$Time <- NULL

# Получаем текущее системное время 
current_time_ct <- Sys.time()
current_time_lt <- as.POSIXlt(current_time_ct)

# Переводим полученное время в формат datetime MS SQL Server: "2023-07-11 19:54:19"
Year4 <- current_time_lt$year + 1900 # номер года (4-значный)
Mon2 <- current_time_lt$mon + 1 # номер месяца
if(Mon2 < 10){
   Mon2 <- paste0('0', Mon2)
   }

Day2 <- current_time_lt$mday  # день месяца
if(Day2 < 10){
   Day2 <- paste0('0', Day2)
   }
   
date <- paste(Year4, Mon2,Day2, sep="-") # Собираем дату воедино

Hour2 <- current_time_lt$hour  # часы
if(Hour2 < 10){
   Hour2 <- paste0('0', Hour2)
   }
   
Min2 <- current_time_lt$min   # минуты
if(Min2 < 10){
   Min2 <- paste0('0', Min2)
   }
   
Sec2 <- current_time_lt$sec   # секунды
Sec2 <- round(Sec2, digits = 0) # доли секунды не выводим, огругляем до ближайшего целого
if(Sec2 < 10){
   Sec2 <- paste0('0', Sec2)
   }
   
time <- paste(Hour2, Min2, Sec2, sep=":") # Собираем дату воедино

# Собираем дату и время вместе, в строку в формате "2023-07-11 19:54:19"
date_time <- paste(date, time) # собираем дату и время в одну переменную

# Добавим колонку 'update_time'
data$update_time <- date_time

# Переименовываем колонки как в базе
names(data) <- c("activity", "region_name", "salary_average", "time_period", "update_time")

# Переставляем колонки местами как в базе
data <- data[, c("activity", "region_name", "time_period", "salary_average", "update_time")]

data$salary_average <-  gsub("\\.", ",", data$salary_average)

# Заменяем десятичную запятую на десятичную точку (американский формат)
# data$salary_average <- str_replace(data$salary_average, "\.", ",")

# подключение требуемых пакетов для СУБД MS SQL
#library(RJDBC)
#library(RMSSQL)
#library(DBI)
library(odbc)

# подключение к MS SQL
# con <- dbConnect(MSSQLServer(),
                # host     = '000.000.000.000',
                # user     = 'FedStat',
                # password = '********',
                # dbname   = "imports")
			
# Установленный драйвер Microsoft ODBC:			
# msodbcsql18-18.2.2.1-1.x86_64.rpm
# Общий размер 442 kB/s | 920 kB     00:02
# предупреждение: /var/cache/dnf/packages-microsoft-com-prod-687ce8c973efa9d3/packages/msodbcsql18-18.2.2.1-1.x86_64.rpm: 
# Заголовок V4 RSA/SHA256 Signature, key ID be1229cf: NOKEY
# packages-microsoft-com-prod 5.0 kB/s | 983  B     00:00
# Импорт GPG-ключа 0xBE1229CF:
# Идентификатор пользователя:  "Microsoft (Release signing) <gpgsecurity@microsoft.com>"
# Отпечаток: BC52 8686 B50D 79E3 39D3 721C EB3E 94AD BE12 29CF
# Источник:  https://packages.microsoft.com/keys/microsoft.asc

# yum install -y mssql-tools18
# Последняя проверка окончания срока действия метаданных: 0:04:19 назад, Пт 28 июл 2023 12:40:57.
# Установка: mssql-tools18 x86_64 18.2.1.1-1 packages-microsoft-com-prod 292 k
# Объем загрузки: 292 k
# Объем изменений: 718 k
# Загрузка пакетов:
# mssql-tools18-18.2.1.1-1.x86_64.rpm 697 kB/s | 292 kB     00:00
# Общий размер 693 kB/s | 292 kB     00:00
# Тест транзакции проведен успешно.
  # Запуск скриптлета: mssql-tools18-18.2.1.1-1.x86_64 
# The license terms for this product can be downloaded from
# http://go.microsoft.com/fwlink/?LinkId=746949 and found in
# /usr/share/doc/mssql-tools18/LICENSE.txt . By entering 'YES',
# you indicate that you accept the license terms.

# Получить список всех установленных на вашем ПК драйверов также можно с помощью функции odbcListDrivers()

# If you want to print the last 10 lines, use
# tail(data, 10)
# for the first 10, you could also do
# head(data, 10)
				
con <- dbConnect(drv = odbc(),
                 #Driver   = "SQL Server",
				 #Driver   = "Free Sybase & MS SQL Driver",
				 Driver   = "ODBC Driver 18 for SQL Server",				 
                 Server   = "000.000.000.000",
                 Database = "imports",
                 UID      = "FedStat",
                 PWD      = "********",
				 Encrypt = "yes",
                 TrustServerCertificate = "yes",
                 Port     = 1433)				
 
# dbSendQuery(con,"INSERT INTO salary_stat_test (activity, region_name, time_period, salary_average, update_time) VALUES ('СЕЛЬСКОЕ, ЛЕСНОЕ ХОЗЯЙСТВО, ОХОТА, РЫБОЛОВСТВО И РЫБОВОДСТВО', 'Тестовый регион', '202201', '35736,4', '2023-07-24 12:50:14');")

dbSendQuery(
  con,
  "TRUNCATE TABLE salary_stat_test;"  
  )
  
# chunk_size = 1000 # обрабатываем вот столько строк таблицы в каждом запросе

#odbc::dbWriteTable(con, "salary_stat_test", data[1:3, ]) # Если нужно записать только первые 5 строк из датафрейма
# odbc::dbWriteTable(con, "salary_stat_test", data, overwrite = TRUE)
# full_chunks <- nrow(data) %/% chunk_size
# full_chunks
# remainder <- nrow(data) %% chunk_size
# remainder

# # После каждой вставки будем разрывать соединение и потом опять подключаться
# for(i in 1:full_chunks) {
	# start_num <- ( (i-1)*chunk_size + 1 )
	# start_num
	# end_num <- ( i*chunk_size )
	# end_num
   
    # #odbc::dbWriteTable(con, "salary_stat_test", data[start_num:end_num, ], append = TRUE, batch_rows = getOption("odbc.batch_rows", chunk_size))
    # }
	
# for(i in 1:nrow(data)) {
	# print (i)
	# query <- paste0("INSERT INTO salary_stat_test (activity, region_name, time_period, salary_average) VALUES ('", 
					# data$activity[i], 
					# "', '", data$region_name[i], 
					# "', '", data$time_period[i], 
					# "', '", data$salary_average[i], 
					# "')")
    # dbSendQuery(con, query)
	# }

# start_num <- ( full_chunks*chunk_size + 1 )
# end_num <- full_chunks*chunk_size + remainder
   
# odbc::dbWriteTable(con, "salary_stat_test", data[start_num:end_num, ], append = TRUE, batch_rows = getOption("odbc.batch_rows", remainder))

   

odbc::dbWriteTable(con, "salary_stat_test", data[1:200000, ], append = TRUE, batch_rows = getOption("odbc.batch_rows", 200000))

#sqlSave(con, data, tablename = "salary_stat_test", append = TRUE)

#sqlExecute(con, "EXEC BOinsert @activity = ?, @region_name = ?, @time_period = ?, @salary_average = ?, @update_time = ?", data = data)
#dbSendQuery(con, "EXEC BOinsert @activity = ?, @region_name = ?, @time_period = ?, @salary_average = ?, @update_time = ?", data = data)

# data2 = data.frame(
	# activity = c('СЕЛЬСКОЕ', 'ЛЕСНОЕ', 'ХОЗЯЙСТВО', 'ОХОТА', 'РЫБОЛОВСТВО'), 
	# region_name = c('Тестовый регион1', 'Тестовый регион2', 'Тестовый регион3', 'Тестовый регион4', 'Тестовый регион5'), 
	# time_period = c('202201', '202202', '202203', '202204', '202205'), 
	# salary_average = c('35736,1', '35736,2', '35736,3', '35736,4', '35736,5'), 
	# update_time = c('2023-07-22 12:50:11', '2023-07-22 12:50:12', '2023-07-24 12:50:13', '2023-07-24 12:50:14', '2023-07-24 12:50:15')
	# )

# разрыв соединения с БД
dbDisconnect(con)

#sqlSave(con, data, tablename = "salary_stat_test", append = TRUE)

# Если нужно вывести полученную таблицу на экран
# View(data)

# выводим полученную таблицу в csv-файл
# формат csv2 подразумевает разделитель полей не ',' а ';'
# а разделитель десятичных разрядов - не '.' а ',' 
#write.csv(data, "/home/vova/data_2023-07-14_14-07.csv", csv2 = TRUE, row.names = FALSE, fileEncoding = "UTF-8") #Linux
write.csv(data, "C:\\Users\\vovaadmin\\Documents\\ПРОЕКТЫ\\СКИТ.ПАСПОРТИЗАЦИЯ\\Зарплаты\\data_2023-08-28_12-19.csv") #Windows

# write.csv(data, "C:\\Users\\vovaadmin\\Documents\\ПРОЕКТЫ\\СКИТ.ПАСПОРТИЗАЦИЯ\\Зарплаты\\data_2023-07-13_17-49.csv", csv2 = TRUE, row.names = FALSE, fileEncoding = "UTF-8") 


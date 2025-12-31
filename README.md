# KVStore — تمرین سوم سیستم‌های توزیع‌شده 

در این مرحله از پروژه به افزودن قابلیت **Replication** است. یک پایگاه داده **کلید-مقدار** توزیع‌شده با تضمین سازگاری قوی **(strong consistency)** که از پروتکل تایید دو مرحله‌ای **(2PC)** برای تکثیر استفاده می‌کند.
از ویزگی های آن می‌توان به:
.
1. `**معماری Primary–Backup** : یک نود Primary مسئول عملیات نوشتن است و چند نود Backup برای خواندن داده‌ها وجود دارد.`
2. `**پروتکل تعهد دو مرحله‌ای (2PC)**: تراکنش‌های توزیع‌شدهٔ اتمیک برای تضمین سازگاری قوی.`
3. `**ارتباط gRPC**: استفاده از پروتکل باینری سریع و کارا برای ارتباط بین نودها.`
4. `**پشتیبانی از Collection**: امکان سازمان‌دهی داده‌ها در مجموعه‌های نام‌دار.`
5. `**ذخیره‌سازی پایدار**: استفاده از فایل‌های JSONL به‌صورت append-only برای دوام داده‌ها.`

 ## معماری

### جریان پروتکل تعهد دو مرحله‌ای (2PC)

1. **مرحله آماده‌سازی (Prepare Phase)**:

- نود Primary پیام PREPARE را به همهٔ Replicaها ارسال می‌کند..
- هر Replica درخواست نوشتن را اعتبارسنجی کرده و رأی YES یا NO می‌دهد..
- رپلیکا تا زمان تصمیم نهایی، تراکنش را قفل می‌کند.

2. **مرحلهٔ Commit یا Abort**:

- اگر همه کپی‌ها به بله رأی دهند: Primary به‌صورت محلی commit می‌کند.، پیام COMMIT به همه ارسال می‌شود..
- اگر حتی یکی NO بدهد: Primary پیام ABORT را به همه ارسال می‌کند.
- عملیات نوشتن فقط زمانی موفق است که همهٔ نودها commit کنند.

تضمین‌ها:
- **اتمی بودن (Atomicity)**: نوشتن یا روی همهٔ نودها انجام می‌شود یا روی هیچ‌کدام
- **سازگاری قوی (Strong Consistency)**: همهٔ Replicaها دادهٔ یکسان دارند
- **دوام (Durability)**: دادهٔ commit‌شده به‌صورت پایدار ذخیره می‌شود>

## اجرای پروژه

## 1) پیش‌نیازها
- **Go 1.24+**
- **Docker و Docker Compose**
- **Protocol Buffers**

---
### 2) شروع سریع

```bash
make run
make stop
make clean
make build
make proto
```

--

## 3) اجرای محلی (بدون Docker)

### 2.1. اجرا
```bash
go run ./main.go

# Windows - PowerShell
$env:APP_ADDR=":8080"; $env:DATA_DIR="$PWD\data"; go run .\main.go

# Windows - CMD
set APP_ADDR=:8080 & set DATA_DIR=%cd%\data & go run .\main.go
```
پس از اجرا باید ببینید:
```
listening on :8080 (data: ./data)
```

### 2.2. تست سریع با curl
> برای دیدن **Status-Line** مثل `HTTP/1.1 200 OK` از سویچ `-i` استفاده کنید.

- **ذخیره** (کالکشن پیش‌فرض):
```bash
curl -i -X PUT "http://localhost:8080/objects" \
  -H "Content-Type: application/json" \
  --data '{"key":"user:1234","value":{"name":"Amin Alavli","age":23,"email":"a.alavi@fum.ir"}}'
```

- **گرفتن با کلید** (وجود دارد → 200 OK):
```bash
curl -i "http://localhost:8080/objects/user:1234"
```
خروجی نمونه:
```
HTTP/1.1 200 OK
Content-Type: application/json
...
{"name":"Amin Alavli","age":23,"email":"a.alavi@fum.ir"}
```

- **گرفتن کلید ناموجود** (→ 404 Not Found):
```bash
curl -i "http://localhost:8080/objects/user:9999"
```

- **لیست همهٔ اشیاء** :
```bash
# همهٔ کالکشن‌ها با نمایش نام کالکشن
curl "http://localhost:8080/objects?limit=200&includeCollection=true"

# فقط کالکشن users
curl "http://localhost:8080/objects?collection=users&limit=50&offset=0"

# فیلتر پیشوندی (مثلاً کلیدهایی که با user: شروع می‌شوند)
curl "http://localhost:8080/objects?prefix=user:&limit=200&includeCollection=true"

# گرفتن یک محصول خاص
curl.exe "http://localhost:8080/objects/p:1001?collection=products"
```

---

## 3) پیکربندی نودها

### نود Primary
- پورت HTTP: 8080
- پورت gRPC: 50051
- هماهنگ‌کننده عملیات نوشتن با 2PC
- مسئول تکثیر داده‌ها به Replicaها

### نودهای Backup
- Backup1: پورت 8081
- Backup2: پورت 8082
- شرکت در رأی‌دهی پروتکل 2PC
- سرویس‌دهی به درخواست‌های خواندن
- رد درخواست‌های نوشتن مستقیم

---
## 4) ذخیره‌سازی داده‌ها

داده‌ها در مسیرهای زیر ذخیره می‌شوند:
- `./data-primary/`
- `./data-backup1/`
- `./data-backup2/`

هر Collection در فایل `<collection>.jsonl` ذخیره می‌شود.

---

## 5) اجرای Docker

### 5.1. ساخت ایمیج
```bash
docker build -t kvstore_2:opt .
```

### 5.2. اجرا با پایداری داده
```bash
# Windows - PowerShell
docker run --rm -p 8080:8080 -e DATA_DIR=/data -v "${PWD}\data:/data" kvstore:opt

# Windows - CMD
docker run --rm -p 8080:8080 -e DATA_DIR=/data -v "%cd%\data:/data" kvstore:opt
```
> با `-v` داده‌ها روی دیسک میزبان ذخیره می‌شوند و بعد از توقف کانتینر باقی می‌مانند.

### 5.3. تست داخل Docker
مثل اجرای محلی:
```bash
curl -i "http://localhost:8080/objects?limit=1"
curl -i -X PUT "http://localhost:8080/objects" -H "Content-Type: application/json" \
  --data '{"key":"user:1234","value":{"name":"Amin Alavli","age":23,"email":"a.alavi@fum.ir"}}'
```
```bash
curl.exe -X PUT "http://localhost:8080/objects?collection=products" -H "Content-Type: application/json"
--data "{\"key\":\"p:1001\",\"value\":{\"title\":\"Keyboard\",\"price\":39.9,\"stock\":15,\"tags\":[\"peripheral\",\"wired\"]}}"
```

---

## 6) API (خلاصه)

### PUT `/objects`
- **Body** (`application/json`):
```json
{"key":"<string>","value": <valid-json>}
```
- **Query**: `collection=<name>` اختیاری (پیش‌فرض: `default`)
- **Response**: `HTTP/1.1 200 OK` (بدنهٔ پیش‌فرض ساده: `{"status":"ok"}`)

### GET `/objects/{key}`
- **Query**: `collection=<name>` اختیاری (اگر در کالکشنی ذخیره کرده باشیم همان را می دهیم)
- **Response**:
  - موجود: `200 OK` + مقدار JSON خام
  - ناموجود: `404 Not Found`

### GET `/objects`
- **Query**:
  - `collection=<name>` (اختیاری؛ اگر ندهیم همهٔ کالکشن‌ها لیست می‌شوند)
  - `limit` (۱..۱۰۰۰۰، پیش‌فرض ۱۰۰) و (`offset` ≥۰)
  - `prefix` برای فیلتر پیشوندی روی کلید
  - `includeCollection=true` برای نمایش نام کالکشن کنار هر آیتم
- **Response**: آرایهٔ JSON به‌صورت **استریم** (حافظهٔ ثابت در مقیاس بزرگ)

---



## 7) اندازهٔ ایمیج و بهینه‌سازی
**Dockerfile چند مرحله ای**

  مرحله build روی `golang:1.24-alpine`و مرحله نهایی روی `scratch` برای کمینه کردن سایز انجام میشود.

**بااینری استاتیک و کم‌حجم**: با CGO_ENABLED=0, GOOS=linux, GOARCH=amd64 و -trimpath -ldflags "-s -w" کامپایل می‌شود. 

**اجرای امن (nonroot) و بدون شِل**:در stage نهایی `USER 65532:65532` استفاده شده و چون base scratch است، شِل یا ابزار اضافی وجود ندارد (surface کوچک‌تر).

**پیکربندی سادهٔ اجرا متغیرها**:APP_ADDR=:8080 و DATA_DIR=/app/data؛ پورت سرویس EXPOSE 8080.

> برای نمایش سایز ایمیج:
```bash
docker images kvstore:opt
```
---
## 8) نمونه های تست شده

نمونه اول:
```bash
curl "http://localhost:8080/objects"
```
خروجی نمونه:
```
[
  {"key":"user:1234","value":{"name":"Amin Alavli","age":23,"email":"a.alavi@fum.ir"}},
  {"key":"p:1001","value":{"title":"Keyboard","price":39.9,"stock":15,"tags":["peripheral","wired"]}},
  {"key":"p:1002","value":{"title":"Wireless Mouse","price":24.5,"stock":40,"tags":["peripheral","wireless"]}},
  {"key":"p:1003","value":{"title":"Monitor 24","price":129.0,"stock":8,"tags":["display","24inch","1080p"]}},
  {"key":"p:1004","value":{"title":"USB-C Cable","price":5.9,"stock":100,"tags":["cable","usb-c"]}},
  {"key":"p:1005","value":{"title":"mouse wireless","price":8.9,"stock":50,"tags":["mouse","wireless"]}},
  {"key":"u:1","value":{"name":"Sara","age":21}},
  {"key":"u:2","value":{"name":"Amin","age":25}},
  {"key":"u:3","value":{"name":"Reza"}}
]
```
نمونه دوم:
```bash
curl "http://localhost:8080/objects?collection=products"
```
خروجی نمونه:
```
[
  {"key":"p:1001","value":{"title":"Keyboard","price":39.9,"stock":15,"tags":["peripheral","wired"]}},
  {"key":"p:1002","value":{"title":"Wireless Mouse","price":24.5,"stock":40,"tags":["peripheral","wireless"]}},
  {"key":"p:1003","value":{"title":"Monitor 24","price":129.0,"stock":8,"tags":["display","24inch","1080p"]}},
  {"key":"p:1004","value":{"title":"USB-C Cable","price":5.9,"stock":100,"tags":["cable","usb-c"]}},
  {"key":"p:1005","value":{"title":"mouse wireless","price":8.9,"stock":50,"tags":["mouse","wireless"]}}
]
```
نمونه سوم:
```bash
curl -i "http://localhost:8080/objects/user:1234"
```
خروجی نمونه:
```
HTTP/1.1 200 OK
Content-Type: application/json
Date: Fri, 07 Nov 2025 11:13:17 GMT
Content-Length: 56

{"name":"Amin Alavli","age":23,"email":"a.alavi@fum.ir"}
```
نمونه چهارم:
```bash
curl -i -X PUT "http://localhost:8080/objects?collection=users" ^
  -H "Content-Type: application/json" ^
  -d "{\"key\":\"user:ali\",\"value\":{\"name\":\"Ali\",\"age\":22}}"
```
خروجی نمونه:
```
HTTP/1.1 200 OK
Content-Type: application/json
Date: Fri, 07 Nov 2025 11:19:45 GMT
Content-Length: 15

{"status":"ok"}
```
نمونه پنجم:
```bash
curl -i "http://localhost:8080/objects/user:12"
```
خروجی نمونه:
```
HTTP/1.1 404 Not Found
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff
Date: Fri, 07 Nov 2025 15:18:12 GMT
Content-Length: 19

404 page not found
```
## 9) نسخه
v0.1.0 - پیاده‌سازی پروتکل Two-Phase Commit

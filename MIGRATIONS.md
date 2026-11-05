# Database Migrations

В проекте используется Goose для управления миграциями PostgreSQL.

## Структура

Все миграции находятся в директории migrations/.

Формат имени:

0001_init.sql
0002_channels.sql

Каждая миграция содержит две секции:

- Up — применение изменений.
- Down — откат изменений.

## Настройка

Для подключения используется переменная окружения:

SPM_DATABASE_URL

По умолчанию:

postgres://spm:spm@localhost:5432/spm?sslmode=disable

## Запуск PostgreSQL

docker compose up -d postgres

## Применение миграций

go run ./cmd/migrate up

## Применение одной миграции

go run ./cmd/migrate up-by-one

## Откат последней миграции

go run ./cmd/migrate down

Перед откатом убедитесь, что это безопасно для данных.
Откат миграции 0002_channels.sql удаляет таблицу channels.

## Проверка состояния

go run ./cmd/migrate status

## Текущая версия БД

go run ./cmd/migrate version

## Работа со старой базой

Если база была создана предыдущей версией Docker Compose
и уже содержит таблицы проекта, необходимо выполнить:

go run ./cmd/migrate baseline

Команда проверит наличие обязательных столбцов,
индексов и стандартных платёжных каналов.

После регистрации версии 2 можно использовать
обычные команды Goose.

Не запускайте baseline на пустой базе данных.

## Добавление новой миграции

Установите Goose CLI:

go install github.com/pressly/goose/v3/cmd/goose@v3.29.0

Создайте миграцию:

goose -dir ./migrations create add_new_column sql

После создания файла добавьте SQL в секции Up и Down.

Пример:

-- +goose Up

ALTER TABLE payments
ADD COLUMN description TEXT;

-- +goose Down

ALTER TABLE payments
DROP COLUMN description;

## Рекомендации

1. Не изменяйте старые миграции после их применения в общей среде.
2. Для каждого нового изменения создавайте отдельный SQL-файл.
3. Проверяйте Up и Down перед отправкой изменений.
4. Не запускайте down без понимания последствий.
5. В CI миграции должны выполняться до интеграционных тестов.
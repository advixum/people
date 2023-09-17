# Система асинхронной обработки сообщений брокера Kafka, запросов API и GraphQL

Тестовое задание: https://github.com/advixum/people/blob/main/TASKS.md <br>
Коллекции Postman <br>
API: https://github.com/advixum/people/blob/main/API.postman_collection.json <br>
GraphQL: https://github.com/advixum/people/blob/main/GraphQL.postman_collection.json <br>

Cервис получает поток сообщений из Apache Kafka, обогащает их через API
и сохраняет данные в базу Postgres. Содержит CRUD обработчики Gin и
GraphQL-Go. Данные кэшируются в Redis. Используется система
логгирования Logrus. Переменные учетных данных вынесены в файл .env.

\* Кэширование в Redis встроено, но не целесообразно, так как потенциальное количество операций записи больше, чем операций чтения.
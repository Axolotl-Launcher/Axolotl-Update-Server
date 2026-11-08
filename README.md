# Axolotl Update Server (Go)

Gin-based update service. PostgreSQL is the production database; SQLite is for local development. This repository replaces the Python service and does not include an analytics dashboard.

Implementation is delivered in staged commits: database foundation/import, update and release APIs, request analytics, then deployment/documentation and verification.


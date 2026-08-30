FROM python:3.12-slim
WORKDIR /srv/app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
ENV UPDATE_SERVER_HOST=0.0.0.0 UPDATE_SERVER_PORT=8082
EXPOSE 8082
CMD ["gunicorn", "--bind", "0.0.0.0:8082", "wsgi:app"]

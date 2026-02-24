FROM python:3.12-slim

WORKDIR /app

RUN apt-get update && \
    apt-get install -y --no-install-recommends clamdscan && \
    rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY config.py platforms.py db.py app.py clamd.conf ./
COPY sources/ sources/
COPY templates/ templates/

EXPOSE 5001

CMD ["python", "app.py"]

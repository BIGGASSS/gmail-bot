FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    UV_LINK_MODE=copy

WORKDIR /app

RUN pip install --no-cache-dir uv

COPY . .
RUN uv sync --no-dev

EXPOSE 8080

CMD ["uv", "run", "gmail-bot"]

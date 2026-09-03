FROM python:3.11.9-slim-bookworm@sha256:2856e6af199e8128161abd320575eb9b341f3b76f017b5d0c9cd364f60d8a050

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHONHASHSEED=0 \
    TRANSFORMERS_OFFLINE=1 \
    HF_HUB_OFFLINE=1

WORKDIR /app

COPY requirements.lock ./requirements.lock
RUN python -m pip install --no-cache-dir --require-hashes -r requirements.lock \
    && python -c "import semantica; from semantica.context import ContextGraph; assert semantica.__version__ == '0.6.5'; ContextGraph()"

COPY app.py ./app.py
COPY inventory.py ./inventory.py

USER 65532:65532
EXPOSE 8080
CMD ["python", "/app/app.py"]

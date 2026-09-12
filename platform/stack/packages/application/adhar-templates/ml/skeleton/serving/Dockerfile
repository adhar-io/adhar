# Fallback for the kaniko path; the platform's app-ci pipeline prefers Cloud
# Native Buildpacks (no Dockerfile needed) and auto-detects Python from
# requirements.txt.
FROM python:3.12-slim
WORKDIR /srv
COPY serving/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt
COPY serving ./serving
ENV PORT=8080
EXPOSE 8080
USER 65532:65532
CMD ["sh", "-c", "uvicorn serving.main:app --host 0.0.0.0 --port ${PORT}"]

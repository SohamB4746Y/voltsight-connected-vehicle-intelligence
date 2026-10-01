{{- define "vs.labels" -}}
app.kubernetes.io/name: voltsight
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{- define "vs.env" -}}
- { name: KAFKA_BROKERS, value: {{ .Values.config.kafkaBrokers | quote }} }
- { name: REDIS_ADDR, value: {{ .Values.config.redisAddr | quote }} }
- { name: CLICKHOUSE_ADDR, value: {{ .Values.config.clickhouseAddr | quote }} }
- { name: POSTGRES_HOST, value: {{ .Values.config.postgresHost | quote }} }
- { name: POSTGRES_PORT, value: {{ .Values.config.postgresPort | quote }} }
- { name: OIDC_ISSUER, value: {{ .Values.config.oidcIssuer | quote }} }
- { name: OIDC_JWKS, value: {{ .Values.config.oidcJwks | quote }} }
- { name: VAULT_ADDR, value: {{ .Values.config.vaultAddr | quote }} }
{{- end -}}

{{- define "vs.secretEnv" -}}
{{- range $k := list "POSTGRES_PASSWORD" "DB_APP_PASSWORD" "DB_GATEWAY_PASSWORD" "DB_ALERTS_PASSWORD" "DB_BATCH_PASSWORD" "DB_PRIVACY_PASSWORD" "DB_SEALER_PASSWORD" "REDIS_PASSWORD" "CLICKHOUSE_PASSWORD" "VAULT_DEV_TOKEN" }}
- name: {{ $k }}
  valueFrom: { secretKeyRef: { name: {{ $.Values.secrets.existingSecret }}, key: {{ $k }}, optional: true } }
{{- end }}
{{- end -}}

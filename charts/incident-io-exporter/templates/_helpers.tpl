{{/*
Expand the name of the chart.
*/}}
{{- define "incident-io-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "incident-io-exporter.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart label.
*/}}
{{- define "incident-io-exporter.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "incident-io-exporter.labels" -}}
helm.sh/chart: {{ include "incident-io-exporter.chart" . }}
{{ include "incident-io-exporter.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "incident-io-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "incident-io-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "incident-io-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "incident-io-exporter.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Secret name holding the API key.
*/}}
{{- define "incident-io-exporter.secretName" -}}
{{- if .Values.incidentio.existingSecret }}
{{- .Values.incidentio.existingSecret }}
{{- else }}
{{- include "incident-io-exporter.fullname" . }}
{{- end }}
{{- end }}

{{/*
Secret key holding the API key.
*/}}
{{- define "incident-io-exporter.secretKey" -}}
{{- if .Values.incidentio.existingSecret }}
{{- .Values.incidentio.existingSecretKey }}
{{- else -}}
api-key
{{- end }}
{{- end }}

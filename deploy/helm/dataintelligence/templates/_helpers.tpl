{{- define "dataintelligence.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dataintelligence.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "dataintelligence.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dataintelligence.labels" -}}
app.kubernetes.io/name: {{ include "dataintelligence.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "dataintelligence.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dataintelligence.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

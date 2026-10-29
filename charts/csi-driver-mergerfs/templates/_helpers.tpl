{{- define "csi-driver-mergerfs.driverName" -}}mergerfs.csi.k8s.io{{- end }}

{{- define "csi-driver-mergerfs.fullname" -}}
{{- if contains .Chart.Name .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}{{ printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}

{{- define "csi-driver-mergerfs.labels" -}}
app.kubernetes.io/name: csi-driver-mergerfs
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{- define "csi-driver-mergerfs.nodeSelectorLabels" -}}
app.kubernetes.io/name: csi-driver-mergerfs
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: node
{{- end }}

{{- define "csi-driver-mergerfs.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- if .Values.image.digest }}{{ printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}{{ printf "%s:%s" .Values.image.repository $tag }}{{ end }}
{{- end }}

{{- define "csi-driver-mergerfs.pluginDir" -}}
{{ .Values.kubeletDir }}/plugins/{{ include "csi-driver-mergerfs.driverName" . }}
{{- end }}

{{- define "csi-driver-mergerfs.controllerSelectorLabels" -}}
app.kubernetes.io/name: csi-driver-mergerfs
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end }}

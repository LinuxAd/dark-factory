{{/* Required values. Every template that needs one of these includes it, so a
missing value fails the render instead of deploying a broken store path. */}}
{{- define "factory.platformName" -}}
{{- required "platformName is required (platform-project var.name)" .Values.platformName -}}
{{- end -}}

{{- define "factory.region" -}}
{{- required "region is required" .Values.region -}}
{{- end -}}

{{- define "factory.bucket" -}}
{{- required "bucket is required (${platformName}-factory-state-${account_id}-${region})" .Values.bucket -}}
{{- end -}}

{{- define "factory.labels" -}}
app.kubernetes.io/part-of: factory
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/* Image names, all derived from the injected image.repository. */}}
{{- define "factory.imageTag" -}}
{{- required "image.tag is required (Argo CD sets it)" .Values.image.tag -}}
{{- end -}}

{{- define "factory.repository" -}}
{{- required "image.repository is required (Argo CD sets it)" .Values.image.repository -}}
{{- end -}}

{{- define "factory.gatewayImage" -}}
{{- printf "%s:%s" (include "factory.repository" .) (include "factory.imageTag" .) -}}
{{- end -}}

{{- define "factory.runnerImage" -}}
{{- printf "%s-runner:%s" (include "factory.repository" .) (include "factory.imageTag" .) -}}
{{- end -}}

{{- define "factory.egressImage" -}}
{{- printf "%s-egress:%s" (include "factory.repository" .) (include "factory.imageTag" .) -}}
{{- end -}}

{{/* Pod and container security contexts that satisfy Pod Security restricted. */}}
{{- define "factory.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "factory.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
capabilities:
  drop:
    - ALL
{{- end -}}

{{/* Egress rule for the EKS Pod Identity agent (link-local, port 80). VPC CNI
policy is assumed to apply to it (docs/research/phase0-spikes.md, item 7). */}}
{{- define "factory.podIdentityAgentEgress" -}}
- to:
    - ipBlock:
        cidr: 169.254.170.23/32
    {{- if .Values.dualStack }}
    - ipBlock:
        cidr: fd00:ec2::23/128
    {{- end }}
  ports:
    - { protocol: TCP, port: 80 }
{{- end -}}

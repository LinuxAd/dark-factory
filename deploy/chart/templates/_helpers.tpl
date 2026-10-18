{{/* Store and secret values. The services ApplicationSet passes only
image.repository, image.tag and route.host, so platformName, region and the
account ID are derived from image.repository, which has the form
<account>.dkr.ecr.<region>.amazonaws.com/<name>/services/<service>. Explicit
platformName, region or bucket values override the derived ones. Every template
that needs one of these includes it, so a value that can be neither derived nor
read fails the render instead of deploying a broken store path. */}}
{{- define "factory.ecr" -}}
{{- $repo := .Values.image.repository | default "" -}}
{{- if regexMatch `^[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/[^/]+/services/[^/]+$` $repo -}}
{{- $path := splitList "/" $repo -}}
{{- $host := splitList "." (index $path 0) -}}
account: {{ index $host 0 | quote }}
region: {{ index $host 3 | quote }}
name: {{ index $path 1 | quote }}
{{- end -}}
{{- end -}}

{{- define "factory.platformName" -}}
{{- $ecr := include "factory.ecr" . | fromYaml -}}
{{- $v := .Values.platformName | default $ecr.name -}}
{{- if not $v -}}
{{- fail (printf "cannot derive platformName (platform-project var.name): set it explicitly, or set image.repository to the ECR form <account>.dkr.ecr.<region>.amazonaws.com/<name>/services/factory (got %q)" (.Values.image.repository | default "")) -}}
{{- end -}}
{{- $v -}}
{{- end -}}

{{- define "factory.region" -}}
{{- $ecr := include "factory.ecr" . | fromYaml -}}
{{- $v := .Values.region | default $ecr.region -}}
{{- if not $v -}}
{{- fail (printf "cannot derive region: set it explicitly, or set image.repository to the ECR form <account>.dkr.ecr.<region>.amazonaws.com/<name>/services/factory (got %q)" (.Values.image.repository | default "")) -}}
{{- end -}}
{{- $v -}}
{{- end -}}

{{/* Same formula as platform-project terraform/01-baseline/factory.tf:
${var.name}-factory-state-${account_id}-${var.region}. */}}
{{- define "factory.bucket" -}}
{{- if .Values.bucket -}}
{{- .Values.bucket -}}
{{- else -}}
{{- $ecr := include "factory.ecr" . | fromYaml -}}
{{- if not $ecr.account -}}
{{- fail (printf "cannot derive bucket (<name>-factory-state-<account>-<region>): set it explicitly, or set image.repository to the ECR form <account>.dkr.ecr.<region>.amazonaws.com/<name>/services/factory (got %q)" (.Values.image.repository | default "")) -}}
{{- end -}}
{{- printf "%s-factory-state-%s-%s" (include "factory.platformName" .) $ecr.account (include "factory.region" .) -}}
{{- end -}}
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

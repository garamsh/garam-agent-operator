{{/*
An object's name, `<namePrefix>-<word>`. A Service name is a DNS label of
at most 63 characters that starts with a letter, and a replica's MicroVM
appends `-<index>`, so a name is held to 60.
*/}}
{{- define "garam-agent-operator-gaon.name" -}}
{{- $prefix := toString (required "namePrefix is required: it is the start of every object's name" .root.Values.namePrefix) -}}
{{- if not (regexMatch "^[a-z]([a-z0-9-]*[a-z0-9])?$" $prefix) -}}
{{- fail (printf "namePrefix %q is refused: it is the start of every object's name, a DNS label that starts with a letter" $prefix) -}}
{{- end -}}
{{- $name := printf "%s-%s" $prefix .word -}}
{{- if gt (len $name) 60 -}}
{{- fail (printf "namePrefix %q makes the name %q longer than 60 characters: a Service name is held to 63 and a MicroVM's name adds an index" $prefix $name) -}}
{{- end -}}
{{- $name -}}
{{- end -}}

{{/*
A Service's full name, the only form gaon resolves: no search line is
written, so a short name is not.
*/}}
{{- define "garam-agent-operator-gaon.host" -}}
{{- $zone := toString (required "dnsZone is required: it is the zone of every Service name, and gaon resolves no short name" .root.Values.dnsZone) -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$" $zone) -}}
{{- fail (printf "dnsZone %q is refused: it is the zone of every Service name, lowercase labels joined by single dots" $zone) -}}
{{- end -}}
{{- printf "%s.default.svc.%s" (include "garam-agent-operator-gaon.name" .) $zone -}}
{{- end -}}

{{- define "garam-agent-operator-gaon.selector" -}}
app.kubernetes.io/name: {{ include "garam-agent-operator-gaon.name" . }}
{{- end -}}

{{/*
An image reference as gaon's Node Agent starts one, `<repository>@sha256:`
and 64 hexadecimal digits, with no tag.
*/}}
{{- define "garam-agent-operator-gaon.image" -}}
{{- $v := toString (required (printf "image.%s is required: it is <repository>@sha256:<digest>, the only form gaon starts" .key) (index .root.Values.image .key)) -}}
{{- if not (regexMatch "^[^@:/]+(:[0-9]+)?(/[^@:/]+)*@sha256:[0-9a-f]{64}$" $v) -}}
{{- fail (printf "image.%s must be <repository>@sha256:<64 lowercase hexadecimal digits> with no tag, got %q: gaon refuses an image a digest does not pin" .key $v) -}}
{{- end -}}
{{- $v -}}
{{- end -}}

{{/*
An absolute https URL with a host and no user information, query or
fragment. The value is never echoed: a URL pasted with a password in it
would reach the log.
*/}}
{{- define "garam-agent-operator-gaon.url" -}}
{{- $v := toString (required (printf "%s is required: %s" .path .what) .value) -}}
{{- $u := urlParse $v -}}
{{- if or (ne $u.scheme "https") (not $u.host) (regexMatch "^[^:]*://[^/]*@" $v) $u.opaque (contains "?" $v) (contains "#" $v) -}}
{{- fail (printf "%s must be an absolute https URL with a host and no user information, query or fragment" .path) -}}
{{- end -}}
{{- $v -}}
{{- end -}}

{{/* A required string that is not a Kubernetes-style name or URL. */}}
{{- define "garam-agent-operator-gaon.text" -}}
{{- toString (required (printf "%s is required: %s" .path .what) .value) -}}
{{- end -}}

{{/*
The reference to the Secret or ConfigMap key a value comes from, written
under a `valueFrom`. Exactly one of `secret` and `configMap` is set, and a
value that is a private key or a credential (`secretOnly`) names a Secret.
The chart passes no value through, and echoes none.
*/}}
{{- define "garam-agent-operator-gaon.ref" -}}
{{- $secret := toString (default "" .ref.secret) -}}
{{- $configMap := toString (default "" .ref.configMap) -}}
{{- if and $secret $configMap -}}
{{- fail (printf "%s names both a secret and a configMap, want one: the file is one key of one object" .path) -}}
{{- end -}}
{{- if and .secretOnly $configMap -}}
{{- fail (printf "%s names a configMap, and it holds a private key or a credential: name a Secret" .path) -}}
{{- end -}}
{{- if not (or $secret $configMap) -}}
{{- fail (printf "%s is required: it names the Secret%s that holds the file, and the key in it" .path (ternary "" " or ConfigMap" .secretOnly)) -}}
{{- end -}}
{{- $key := required (printf "%s.key is required: it is the key of the object the file is read from" .path) .ref.key -}}
{{- if $secret }}
secretKeyRef:
  name: {{ $secret | quote }}
  key: {{ $key | quote }}
{{- else }}
configMapKeyRef:
  name: {{ $configMap | quote }}
  key: {{ $key | quote }}
{{- end }}
{{- end -}}

{{- define "garam-agent-operator-gaon.env" -}}
- name: {{ .name }}
  value: {{ .value | toString | quote }}
{{- end -}}

{{- define "garam-agent-operator-gaon.envFrom" -}}
- name: {{ .name }}
  valueFrom:
    {{- include "garam-agent-operator-gaon.ref" (dict "path" .path "ref" .ref "secretOnly" .secretOnly) | nindent 4 }}
{{- end -}}

{{/*
A file written into the MicroVM, owned by the MicroVM's user because it
names no owner. A private key is owner-only; 256 is 0400, written in
decimal because a YAML parser may read 0400 as a string.
*/}}
{{- define "garam-agent-operator-gaon.file" -}}
- path: {{ .file }}
  {{- if .secretOnly }}
  mode: 256
  {{- end }}
  valueFrom:
    {{- include "garam-agent-operator-gaon.ref" (dict "path" .path "ref" .ref "secretOnly" .secretOnly) | nindent 4 }}
{{- end -}}

{{/*
A quantity as gaon reads one (lib/api quantity.go): a decimal number and
an optional m, k, M, G, T, Ki, Mi, Gi or Ti. The message opens with the
value's dotted name.
*/}}
{{- define "garam-agent-operator-gaon.quantity" -}}
{{- $v := toString (required (printf "%s is required: gaon's Node Agent refuses a MicroVM whose spec names no cpu or no memory, and the chart holds no figure for any program" .path) .value) -}}
{{- if not (regexMatch "^[0-9]+(\\.[0-9]+)?(m|k|M|G|T|Ki|Mi|Gi|Ti)?$" $v) -}}
{{- fail (printf "%s must be a number with an optional m, k, M, G, T, Ki, Mi, Gi or Ti, got %q: gaon decodes no other form of a quantity" .path $v) -}}
{{- end -}}
{{- $v -}}
{{- end -}}

{{/*
One object's `resources`. The requests are required and have no default:
the scheduler places a MicroVM by them. The limits are optional.
*/}}
{{- define "garam-agent-operator-gaon.resources" -}}
{{- $path := printf "resources.%s" .key -}}
{{- $r := index .root.Values.resources .key -}}
resources:
  requests:
    cpu: {{ include "garam-agent-operator-gaon.quantity" (dict "path" (printf "%s.requests.cpu" $path) "value" $r.requests.cpu) | quote }}
    memory: {{ include "garam-agent-operator-gaon.quantity" (dict "path" (printf "%s.requests.memory" $path) "value" $r.requests.memory) | quote }}
  {{- if or $r.limits.cpu $r.limits.memory }}
  limits:
    {{- with $r.limits.cpu }}
    cpu: {{ include "garam-agent-operator-gaon.quantity" (dict "path" (printf "%s.limits.cpu" $path) "value" .) | quote }}
    {{- end }}
    {{- with $r.limits.memory }}
    memory: {{ include "garam-agent-operator-gaon.quantity" (dict "path" (printf "%s.limits.memory" $path) "value" .) | quote }}
    {{- end }}
  {{- end }}
{{- end -}}

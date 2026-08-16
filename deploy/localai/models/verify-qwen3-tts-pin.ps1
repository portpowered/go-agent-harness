[CmdletBinding()]
param(
    [ValidateSet('Contract', 'Compatibility', 'Live', 'Measure', 'NegativeControls')]
    [string]$Mode = 'Contract',
    [string]$ConfigPath = '',
    [string]$Endpoint = '',
    [string]$ArtifactRoot = '',
    [string]$LegacyArtifactPath = '',
    [string]$AudioPath = '',
    [string]$OutputPath = ''
)

$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
    $ConfigPath = Join-Path -Path (Split-Path -Parent $MyInvocation.MyCommand.Path) -ChildPath 'qwen3-tts-pinned.json'
}

function Fail([string]$Message) {
    throw "TTS_PIN_FAIL: $Message"
}

function Get-Field([object]$Object, [string]$Path) {
    $current = $Object
    foreach ($segment in $Path.Split('.')) {
        if ($null -eq $current) {
            return $null
        }
        $property = $current.PSObject.Properties[$segment]
        if ($null -eq $property) {
            return $null
        }
        $current = $property.Value
    }
    return $current
}

function Is-Placeholder([string]$Value) {
    return $Value -match '(?i)^(latest|unknown|todo|placeholder|changeme|example|none)$' -or
        $Value -match '(?i)<[^>]+>'
}

function Require-String([object]$Config, [string]$Path, [string]$Pattern = '') {
    $value = Get-Field $Config $Path
    if ($null -eq $value -or $value -isnot [string] -or [string]::IsNullOrWhiteSpace($value)) {
        Fail "$Path must be a non-empty string"
    }
    if (Is-Placeholder $value) {
        Fail "$Path contains a placeholder value '$value'"
    }
    if ($Pattern -and $value -notmatch $Pattern) {
        Fail "$Path has malformed value '$value'"
    }
    return $value
}

function Assert-Posted-NumericParam([object]$Config, [string]$Name, [object]$Expected) {
    $path = "probe.request.params.$Name"
    $actual = Require-String $Config $path
    $parsed = 0.0
    $styles = [System.Globalization.NumberStyles]::Float
    $culture = [System.Globalization.CultureInfo]::InvariantCulture
    if (-not [double]::TryParse($actual, $styles, $culture, [ref]$parsed)) {
        Fail "$path must be an invariant-culture numeric string"
    }
    if ($parsed -ne [double]$Expected) {
        Fail "$path must equal probe.generation_options.$Name ($Expected), actual '$actual'"
    }
}

function Require-Integer([object]$Config, [string]$Path, [int64]$Minimum = [int64]::MinValue) {
    $value = Get-Field $Config $Path
    $integerTypes = @([byte], [sbyte], [int16], [uint16], [int32], [uint32], [int64], [uint64])
    if ($null -eq $value -or $integerTypes -notcontains $value.GetType()) {
        Fail "$Path must be an integer"
    }
    if ([int64]$value -lt $Minimum) {
        Fail "$Path must be at least $Minimum"
    }
    return $value
}

function Require-Number([object]$Config, [string]$Path, [double]$Minimum = [double]::NegativeInfinity, [double]$Maximum = [double]::PositiveInfinity) {
    $value = Get-Field $Config $Path
    $numberTypes = @([byte], [sbyte], [int16], [uint16], [int32], [uint32], [int64], [uint64], [single], [double], [decimal])
    if ($null -eq $value -or $numberTypes -notcontains $value.GetType()) {
        Fail "$Path must be a number"
    }
    $number = [double]$value
    if ([double]::IsNaN($number) -or [double]::IsInfinity($number) -or $number -lt $Minimum -or $number -gt $Maximum) {
        Fail "$Path must be between $Minimum and $Maximum"
    }
    return $number
}

function Require-Bool([object]$Config, [string]$Path) {
    $value = Get-Field $Config $Path
    if ($null -eq $value -or $value -isnot [bool]) {
        Fail "$Path must be a boolean"
    }
    return $value
}

function Require-Array([object]$Config, [string]$Path, [int]$MinimumCount = 1) {
    $value = Get-Field $Config $Path
    if ($null -eq $value -or $value -isnot [array] -or $value.Count -lt $MinimumCount) {
        Fail "$Path must be an array with at least $MinimumCount item(s)"
    }
    return $value
}

function Read-Config {
    if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf)) {
        Fail "configuration file not found: $ConfigPath"
    }
    try {
        return (Get-Content -Raw -LiteralPath $ConfigPath | ConvertFrom-Json)
    } catch {
        Fail "configuration is not valid JSON: $($_.Exception.Message)"
    }
}

function Assert-Contract([object]$Config) {
    $schema = Require-Integer $Config 'schema_version' 1
    if ($schema -ne 1) {
        Fail 'schema_version must be exactly 1'
    }
    [void](Require-String $Config 'model_id' '^[a-z0-9][a-z0-9-]+$')

    [void](Require-String $Config 'backend.name' '^[a-z0-9][a-z0-9-]+$')
    [void](Require-String $Config 'backend.localai_version' '^v[0-9]+\.[0-9]+\.[0-9]+$')
    [void](Require-String $Config 'backend.localai_commit' '^[0-9a-f]{40}$')
    [void](Require-String $Config 'backend.localai_image' '^localai/localai@sha256:[0-9a-f]{64}$')
    [void](Require-String $Config 'backend.platform' '^linux/(amd64|arm64)$')
    [void](Require-String $Config 'backend.backend_image' '^quay\.io/go-skynet/local-ai-backends@sha256:[0-9a-f]{64}$')
    [void](Require-String $Config 'backend.upstream' '^https://github\.com/[^/]+/[^/]+$')
    [void](Require-String $Config 'backend.upstream_commit' '^[0-9a-f]{40}$')

    [void](Require-String $Config 'gallery.name' '^[a-z0-9][a-z0-9-]+$')
    [void](Require-String $Config 'gallery.revision' '^v[0-9]+\.[0-9]+\.[0-9]+$')
    [void](Require-String $Config 'gallery.source' '^https://raw\.githubusercontent\.com/.+/gallery/index\.yaml$')
    [void](Require-String $Config 'gallery.install_command' '^local-ai models install [a-z0-9][a-z0-9-]+$')

    [void](Require-String $Config 'model.gallery_name' '^[a-z0-9][a-z0-9-]+$')
    [void](Require-String $Config 'model.runtime_name' '^[a-z0-9][a-z0-9-]+$')
    [void](Require-String $Config 'model.backend' '^[a-z0-9][a-z0-9-]+$')
    [void](Require-String $Config 'model.talker_filename' '\.gguf$')
    [void](Require-String $Config 'model.codec_filename' '\.gguf$')
    if ((Get-Field $Config 'model.backend') -ne (Get-Field $Config 'backend.name')) {
        Fail 'model.backend must equal backend.name'
    }
    if ((Get-Field $Config 'model.gallery_name') -ne (Get-Field $Config 'gallery.name')) {
        Fail 'model.gallery_name must equal gallery.name'
    }

    $artifacts = Require-Array $Config 'model.artifacts' 2
    $roles = @{}
    foreach ($artifact in @($artifacts)) {
        [void](Require-String $artifact 'role' '^(talker|tokenizer)$')
        [void](Require-String $artifact 'filename' '\.gguf$')
        [void](Require-String $artifact 'uri' '^(huggingface|https?)://')
        [void](Require-String $artifact 'sha256' '^[0-9a-f]{64}$')
        $role = Get-Field $artifact 'role'
        if ($roles.ContainsKey($role)) {
            Fail "model.artifacts contains duplicate role '$role'"
        }
        $roles[$role] = $true
    }
    foreach ($requiredRole in @('talker', 'tokenizer')) {
        if (-not $roles.ContainsKey($requiredRole)) {
            Fail "model.artifacts is missing role '$requiredRole'"
        }
    }
    if ((Get-Field $Config 'model.talker_filename') -ne (Get-Field $Config 'model.artifacts')[0].filename -and
        (Get-Field $Config 'model.talker_filename') -ne (($artifacts | Where-Object role -eq 'talker').filename)) {
        Fail 'model.talker_filename does not identify the pinned talker artifact'
    }
    if ((Get-Field $Config 'model.codec_filename') -ne (($artifacts | Where-Object role -eq 'tokenizer').filename)) {
        Fail 'model.codec_filename does not identify the pinned tokenizer artifact'
    }

    [void](Require-String $Config 'audio.container' '^wav$')
    [void](Require-Integer $Config 'audio.sample_rate_hz' 1)
    [void](Require-Integer $Config 'audio.channel_count' 1)
    if ((Get-Field $Config 'audio.channel_count') -gt 2) {
        Fail 'audio.channel_count must be 1 or 2'
    }
    [void](Require-String $Config 'audio.sample_format' '^s16le$')
    [void](Require-String $Config 'audio.rms_normalization')
    [void](Require-Number $Config 'audio.silence_threshold_rms' 0.0000001 0.9999999)
    $minimumDuration = Require-Number $Config 'audio.expected_duration_seconds.min_inclusive' 0.000001
    $maximumDuration = Require-Number $Config 'audio.expected_duration_seconds.max_inclusive' 0.000002
    if ($maximumDuration -le $minimumDuration) {
        Fail 'audio.expected_duration_seconds.max_inclusive must exceed min_inclusive'
    }

    [void](Require-String $Config 'probe.text')
    [void](Require-String $Config 'probe.language')
    [void](Require-Number $Config 'probe.generation_options.seed' 0)
    [void](Require-Number $Config 'probe.generation_options.temperature' 0.000001)
    [void](Require-Integer $Config 'probe.generation_options.top_k' 1)
    [void](Require-Number $Config 'probe.generation_options.top_p' 0.000001 1.0)
    [void](Require-Number $Config 'probe.generation_options.repetition_penalty' 0.000001)
    [void](Require-Integer $Config 'probe.generation_options.max_new_tokens' 1)
    [void](Require-Number $Config 'probe.generation_options.speed' 0.000001)
    [void](Require-String $Config 'probe.generation_options.response_format' '^wav$')
    [void](Require-String $Config 'probe.request.model' '^[a-z0-9][a-z0-9-]+$')
    [void](Require-String $Config 'probe.request.input')
    [void](Require-String $Config 'probe.request.language')
    [void](Require-String $Config 'probe.request.response_format' '^wav$')
    [void](Require-Number $Config 'probe.request.speed' 0.000001)
    foreach ($parameterName in @('temperature', 'top_k', 'top_p', 'repetition_penalty', 'max_new_tokens', 'seed')) {
        Assert-Posted-NumericParam $Config $parameterName (Get-Field $Config "probe.generation_options.$parameterName")
    }
    if ((Get-Field $Config 'probe.request.input') -ne (Get-Field $Config 'probe.text')) {
        Fail 'probe.request.input must equal probe.text'
    }
    if ((Get-Field $Config 'probe.request.model') -ne (Get-Field $Config 'model.runtime_name')) {
        Fail 'probe.request.model must equal model.runtime_name'
    }
    if ((Get-Field $Config 'probe.request.language') -ne (Get-Field $Config 'probe.language')) {
        Fail 'probe.request.language must equal probe.language'
    }
    if ((Get-Field $Config 'probe.request.response_format') -ne (Get-Field $Config 'probe.generation_options.response_format')) {
        Fail 'probe.request.response_format must equal probe.generation_options.response_format'
    }
    if ([double](Get-Field $Config 'probe.request.speed') -ne [double](Get-Field $Config 'probe.generation_options.speed')) {
        Fail 'probe.request.speed must equal probe.generation_options.speed'
    }

    [void](Require-String $Config 'verification.script' '\.ps1$')
    [void](Require-String $Config 'verification.endpoint' '^https?://')
    $endpointTimeout = Require-Number $Config 'verification.endpoint_timeout_seconds' 0.1 5.0
    if ($endpointTimeout -ne [math]::Floor($endpointTimeout)) {
        Fail 'verification.endpoint_timeout_seconds must be a whole number of seconds'
    }
    [void](Require-Number $Config 'verification.generation_timeout_seconds' 1.0 180.0)
    foreach ($commandPath in @('contract_command', 'compatibility_command', 'generation_command', 'measurement_command', 'negative_controls_command')) {
        [void](Require-String $Config "verification.$commandPath")
    }

    [void](Require-String $Config 'compatibility.legacy_artifact' '\.gguf$')
    [void](Require-String $Config 'compatibility.legacy_gallery_uri' '^(huggingface|https?)://')
    [void](Require-String $Config 'compatibility.legacy_gallery_sha256' '^[0-9a-f]{64}$')
    [void](Require-String $Config 'compatibility.conclusion' '^(compatible|incompatible)$')
    [void](Require-String $Config 'compatibility.basis')
    [void](Require-Bool $Config 'compatibility.migration_required')
    [void](Require-String $Config 'compatibility.migration_command')

    Write-Output 'CONTRACT=PASS fields=all-required-fields'
}

function Test-ArtifactHashes([object]$Config, [string]$Root) {
    if ([string]::IsNullOrWhiteSpace($Root)) {
        Write-Output 'ARTIFACT_HASH=SKIP reason=ArtifactRoot-not-provided'
        return
    }
    foreach ($artifact in @($Config.model.artifacts)) {
        $path = Join-Path -Path $Root -ChildPath $artifact.filename
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            Write-Output "ARTIFACT_HASH=SKIP role=$($artifact.role) reason=selected-file-absent path=$path"
            continue
        }
        $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        if ($actual -ne $artifact.sha256) {
            Fail "SHA-256 mismatch for $($artifact.role): expected $($artifact.sha256), actual $actual, path $path"
        }
        Write-Output "ARTIFACT_HASH=PASS role=$($artifact.role) sha256=$actual"
    }
}

function Get-EndpointStatus([string]$BaseUri, [int]$TimeoutSeconds) {
    $uri = $BaseUri.TrimEnd('/') + '/readyz'
    try {
        $response = Invoke-WebRequest -UseBasicParsing -Uri $uri -Method Get -TimeoutSec $TimeoutSeconds
        return [pscustomobject]@{
            Reachable = $true
            Detail = "status=$($response.StatusCode) uri=$uri"
        }
    } catch {
        return [pscustomobject]@{
            Reachable = $false
            Detail = "unreachable uri=$uri error=$($_.Exception.Message)"
        }
    }
}

function Read-WavPcm16([string]$Path, [object]$Config) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        Fail "audio file not found: $Path"
    }
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 12) {
        Fail "audio file is zero-length or shorter than a WAV header: $Path"
    }
    $ascii = [System.Text.Encoding]::ASCII
    if ($ascii.GetString($bytes, 0, 4) -ne 'RIFF' -or $ascii.GetString($bytes, 8, 4) -ne 'WAVE') {
        Fail "audio file is not a RIFF/WAVE file: $Path"
    }

    $offset = 12
    $sampleRate = 0
    $channels = 0
    $bits = 0
    $format = 0
    $dataOffset = -1
    $dataSize = 0
    while ($offset + 8 -le $bytes.Length) {
        $chunkId = $ascii.GetString($bytes, $offset, 4)
        $chunkSize = [int64][System.BitConverter]::ToUInt32($bytes, $offset + 4)
        $chunkStart = $offset + 8
        if ($chunkStart + $chunkSize -gt $bytes.Length) {
            Fail "WAV chunk '$chunkId' exceeds file length: $Path"
        }
        if ($chunkId -eq 'fmt ') {
            if ($chunkSize -lt 16) {
                Fail "WAV fmt chunk is too short: $Path"
            }
            $format = [System.BitConverter]::ToUInt16($bytes, $chunkStart)
            $channels = [System.BitConverter]::ToUInt16($bytes, $chunkStart + 2)
            $sampleRate = [System.BitConverter]::ToInt32($bytes, $chunkStart + 4)
            $bits = [System.BitConverter]::ToUInt16($bytes, $chunkStart + 14)
        } elseif ($chunkId -eq 'data') {
            $dataOffset = $chunkStart
            $dataSize = $chunkSize
        }
        $offset = $chunkStart + $chunkSize
        if (($offset % 2) -ne 0) {
            $offset++
        }
    }

    $expectedRate = [int](Get-Field $Config 'audio.sample_rate_hz')
    $expectedChannels = [int](Get-Field $Config 'audio.channel_count')
    if ($format -ne 1 -or $channels -ne $expectedChannels -or $sampleRate -ne $expectedRate -or $bits -ne 16) {
        Fail "audio contract mismatch: format=$format sample_rate=$sampleRate channels=$channels bits=$bits; expected PCM16/$expectedRate/$expectedChannels/16"
    }
    if ($dataOffset -lt 0 -or $dataSize -le 0 -or ($dataSize % 2) -ne 0) {
        Fail "audio file has no non-empty PCM16 data: $Path"
    }

    $samples = New-Object 'System.Collections.Generic.List[Int16]'
    for ($index = 0; $index -lt $dataSize; $index += 2) {
        [void]$samples.Add([System.BitConverter]::ToInt16($bytes, $dataOffset + $index))
    }
    if ($samples.Count -eq 0) {
        Fail "audio file has zero samples: $Path"
    }
    $sumSquares = 0.0
    foreach ($sample in $samples) {
        $normalized = [double]$sample / 32768.0
        $sumSquares += $normalized * $normalized
    }
    $rms = [math]::Sqrt($sumSquares / $samples.Count)
    $duration = [double]$samples.Count / ([double]$sampleRate * [double]$channels)
    return [pscustomobject]@{
        SampleCount = $samples.Count
        SampleRate = $sampleRate
        Channels = $channels
        RMS = $rms
        DurationSeconds = $duration
    }
}

function Assert-Audio([string]$Path, [object]$Config) {
    $measurement = Read-WavPcm16 $Path $Config
    $threshold = [double](Get-Field $Config 'audio.silence_threshold_rms')
    $minimum = [double](Get-Field $Config 'audio.expected_duration_seconds.min_inclusive')
    $maximum = [double](Get-Field $Config 'audio.expected_duration_seconds.max_inclusive')
    if ($measurement.RMS -le $threshold) {
        Fail ("audio RMS {0:F6} is not strictly above silence threshold {1:F6}" -f $measurement.RMS, $threshold)
    }
    if ($measurement.DurationSeconds -lt $minimum -or $measurement.DurationSeconds -gt $maximum) {
        Fail ("audio duration {0:F6}s is outside inclusive range [{1:F6}s, {2:F6}s]" -f $measurement.DurationSeconds, $minimum, $maximum)
    }
    Write-Output ("AUDIO_PASS path={0} rms={1:F6} duration_seconds={2:F6} threshold={3:F6} range=[{4:F6},{5:F6}]" -f $Path, $measurement.RMS, $measurement.DurationSeconds, $threshold, $minimum, $maximum)
    return $measurement
}

function Write-SilenceWav([string]$Path, [int]$SampleCount, [int]$SampleRate) {
    $stream = New-Object System.IO.MemoryStream
    $writer = New-Object System.IO.BinaryWriter($stream)
    $dataBytes = $SampleCount * 2
    $writer.Write([System.Text.Encoding]::ASCII.GetBytes('RIFF'))
    $writer.Write([int]($dataBytes + 36))
    $writer.Write([System.Text.Encoding]::ASCII.GetBytes('WAVE'))
    $writer.Write([System.Text.Encoding]::ASCII.GetBytes('fmt '))
    $writer.Write([int]16)
    $writer.Write([int16]1)
    $writer.Write([int16]1)
    $writer.Write([int]$SampleRate)
    $writer.Write([int]($SampleRate * 2))
    $writer.Write([int16]2)
    $writer.Write([int16]16)
    $writer.Write([System.Text.Encoding]::ASCII.GetBytes('data'))
    $writer.Write([int]$dataBytes)
    $writer.Write((New-Object byte[] $dataBytes))
    $writer.Flush()
    [System.IO.File]::WriteAllBytes($Path, $stream.ToArray())
    $writer.Dispose()
    $stream.Dispose()
}

function Run-NegativeControls([object]$Config) {
    $directory = Join-Path ([System.IO.Path]::GetTempPath()) ('qwen3-tts-pin-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    try {
        $silentPath = Join-Path $directory 'silent.wav'
        $emptyPath = Join-Path $directory 'empty.wav'
        Write-SilenceWav $silentPath 2400 ([int](Get-Field $Config 'audio.sample_rate_hz'))
        [System.IO.File]::WriteAllBytes($emptyPath, [byte[]]@())

        $silentFailed = $false
        try {
            [void](Assert-Audio $silentPath $Config)
        } catch {
            $silentFailed = $true
            Write-Output "NEGATIVE_CONTROL=PASS name=silent_clip reason=$($_.Exception.Message)"
        }
        if (-not $silentFailed) {
            Fail 'silent clip unexpectedly passed audio validation'
        }

        $emptyFailed = $false
        try {
            [void](Assert-Audio $emptyPath $Config)
        } catch {
            $emptyFailed = $true
            Write-Output "NEGATIVE_CONTROL=PASS name=zero_length_file reason=$($_.Exception.Message)"
        }
        if (-not $emptyFailed) {
            Fail 'zero-length file unexpectedly passed audio validation'
        }
        Write-Output 'NEGATIVE_CONTROLS=PASS silent_clip=failed-validation zero_length_file=failed-validation'
    } finally {
        Remove-Item -LiteralPath $directory -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-TtsRequest([string]$BaseUri, [object]$Request, [int]$TimeoutSeconds, [string]$Path) {
    $client = New-Object System.Net.Http.HttpClient
    $client.Timeout = [TimeSpan]::FromSeconds($TimeoutSeconds)
    try {
        $json = $Request | ConvertTo-Json -Compress -Depth 10
        $content = New-Object System.Net.Http.StringContent($json, [System.Text.Encoding]::UTF8, 'application/json')
        $response = $client.PostAsync($BaseUri.TrimEnd('/') + '/v1/audio/speech', $content).GetAwaiter().GetResult()
        $responseBytes = $response.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
        if (-not $response.IsSuccessStatusCode) {
            $body = [System.Text.Encoding]::UTF8.GetString($responseBytes)
            Fail "TTS request failed status=$([int]$response.StatusCode) body=$body"
        }
        [System.IO.File]::WriteAllBytes($Path, $responseBytes)
        return $Path
    } finally {
        if ($null -ne $client) {
            $client.Dispose()
        }
    }
}

$config = Read-Config

switch ($Mode) {
    'Contract' {
        Assert-Contract $config
        Test-ArtifactHashes $config $ArtifactRoot
        exit 0
    }
    'NegativeControls' {
        Assert-Contract $config
        Run-NegativeControls $config
        exit 0
    }
    'Measure' {
        Assert-Contract $config
        if ([string]::IsNullOrWhiteSpace($AudioPath)) {
            Fail 'Measure requires -AudioPath'
        }
        Assert-Audio $AudioPath $config
        exit 0
    }
    'Compatibility' {
        Assert-Contract $config
        if ([string]::IsNullOrWhiteSpace($LegacyArtifactPath)) {
            $LegacyArtifactPath = [string](Get-Field $config 'compatibility.legacy_artifact')
        }
        if ([string]::IsNullOrWhiteSpace($Endpoint)) {
            $Endpoint = [string](Get-Field $config 'verification.endpoint')
        }
        $endpointStatus = Get-EndpointStatus $Endpoint ([int](Get-Field $config 'verification.endpoint_timeout_seconds'))
        $legacyExists = Test-Path -LiteralPath $LegacyArtifactPath -PathType Leaf
        if (-not $legacyExists) {
            Write-Output "COMPATIBILITY=UNAVAILABLE reason=legacy-artifact-absent path=$LegacyArtifactPath"
            Write-Output "ENDPOINT=$($endpointStatus.Detail)"
            exit 0
        }
        if (-not $endpointStatus.Reachable) {
            Write-Output "COMPATIBILITY=UNAVAILABLE reason=endpoint-unreachable path=$LegacyArtifactPath"
            Write-Output "ENDPOINT=$($endpointStatus.Detail)"
            exit 0
        }
        $legacyHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $LegacyArtifactPath).Hash.ToLowerInvariant()
        $expectedLegacyHash = [string](Get-Field $config 'compatibility.legacy_gallery_sha256')
        if ($legacyHash -ne $expectedLegacyHash) {
            Fail "legacy artifact SHA-256 mismatch: expected $expectedLegacyHash, actual $legacyHash, path $LegacyArtifactPath"
        }
        Write-Output "COMPATIBILITY=NOT_PROVEN reason=legacy-file-present-but-current-endpoint-model-selection-is-required sha256=$legacyHash"
        exit 0
    }
    'Live' {
        Assert-Contract $config
        if ([string]::IsNullOrWhiteSpace($Endpoint)) {
            $Endpoint = [string](Get-Field $config 'verification.endpoint')
        }
        $endpointStatus = Get-EndpointStatus $Endpoint ([int](Get-Field $config 'verification.endpoint_timeout_seconds'))
        if (-not $endpointStatus.Reachable) {
            Write-Output "LIVE_SKIP reason=localai-endpoint-unreachable detail=$($endpointStatus.Detail)"
            exit 0
        }
        if ([string]::IsNullOrWhiteSpace($ArtifactRoot)) {
            $ArtifactRoot = [string]$env:LOCALAI_MODELS_DIR
        }
        if ([string]::IsNullOrWhiteSpace($ArtifactRoot)) {
            Write-Output 'LIVE_SKIP reason=selected-artifact-root-not-provided'
            exit 0
        }
        Test-ArtifactHashes $config $ArtifactRoot
        foreach ($artifact in @($config.model.artifacts)) {
            $selectedPath = Join-Path -Path $ArtifactRoot -ChildPath $artifact.filename
            if (-not (Test-Path -LiteralPath $selectedPath -PathType Leaf)) {
                Write-Output "LIVE_SKIP reason=selected-artifact-absent role=$($artifact.role) path=$selectedPath"
                exit 0
            }
        }
        if ([string]::IsNullOrWhiteSpace($OutputPath)) {
            $OutputPath = Join-Path ([System.IO.Path]::GetTempPath()) ('qwen3-tts-' + [guid]::NewGuid().ToString('N') + '.wav')
        }
        $requestPath = Invoke-TtsRequest $Endpoint $config.probe.request ([int](Get-Field $config 'verification.generation_timeout_seconds')) $OutputPath
        $measurement = Assert-Audio $requestPath $config
        Write-Output ("LIVE_PASS endpoint={0} output={1} rms={2:F6} duration_seconds={3:F6}" -f $Endpoint, $requestPath, $measurement.RMS, $measurement.DurationSeconds)
        exit 0
    }
}

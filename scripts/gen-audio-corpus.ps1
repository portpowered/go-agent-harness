[CmdletBinding()]
param(
    [Alias('BinaryPath', 'CliPath')]
    [string]$TtsBinary = '',
    [Alias('ModelDir')]
    [string]$ModelDirectory = '',
    [Alias('OutputDir')]
    [string]$OutputDirectory = (Join-Path $PSScriptRoot '..\go-agent-loop\testdata\audio'),
    [Alias('VoiceName')]
    [string]$Voice = 'default',
    [string]$ReferenceAudio = ''
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$VadThreshold = [double]300.0
$MaxCorpusBytes = [int64](25 * 1024 * 1024)
$RequiredRates = @(16000, 24000)
$RequiredClasses = @('utt_short', 'utt_long', 'silence', 'noise', 'overlap', 'multi_utt', 'truncated', 'tool_request')
$ReferenceAudioPath = $null
function Fail([string]$Message) { throw "AUDIO_CORPUS_FAIL: $Message" }
function Resolve-InputPath([string]$Path, [string]$Label) {
    if ([string]::IsNullOrWhiteSpace($Path)) { Fail "$Label is required" }
    if ([IO.Path]::IsPathRooted($Path)) { return [IO.Path]::GetFullPath($Path) }
    return [IO.Path]::GetFullPath((Join-Path (Get-Location).Path $Path))
}
function Assert-FileInput([string]$Path, [string]$Label) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { Fail "$Label not found: $Path" }; $item = Get-Item -LiteralPath $Path
    if ($item.Length -le 0) { Fail "$Label is empty: $Path" }
}
function Assert-ModelDirectory([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { Fail "model directory not found: $Path" }; $modelFiles = @(Get-ChildItem -LiteralPath $Path -File -Recurse -ErrorAction SilentlyContinue)
    if ($modelFiles.Count -eq 0) { Fail "model directory contains no files: $Path" }
}
function Get-UInt32([byte[]]$Bytes, [int]$Offset) { return [uint64][BitConverter]::ToUInt32($Bytes, $Offset) }
function Read-Pcm16Wav([string]$Path) {
    Assert-FileInput $Path 'WAV file'
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 44) { Fail "WAV is shorter than its header: $Path" }
    $ascii = [Text.Encoding]::ASCII
    if ($ascii.GetString($bytes, 0, 4) -ne 'RIFF' -or $ascii.GetString($bytes, 8, 4) -ne 'WAVE') {
        Fail "WAV is not RIFF/WAVE: $Path"
    }
    $riffEnd = 8 + [int64](Get-UInt32 $bytes 4)
    if ($riffEnd -ne $bytes.Length) { Fail "WAV RIFF size does not match file length: $Path" }
    $offset = 12
    $fmt = $null
    $dataOffset = -1
    $dataSize = 0L
    while ($offset -lt $riffEnd) {
        if ($offset + 8 -gt $riffEnd) { Fail "WAV has a truncated chunk header: $Path" }
        $chunkId = $ascii.GetString($bytes, [int]$offset, 4)
        $chunkSize = [int64](Get-UInt32 $bytes ([int]$offset + 4))
        $chunkStart = $offset + 8
        $chunkEnd = $chunkStart + $chunkSize
        if ($chunkEnd -gt $riffEnd) { Fail "WAV chunk '$chunkId' exceeds file length: $Path" }
        if ($chunkId -eq 'fmt ') {
            if ($null -ne $fmt) { Fail "WAV has duplicate fmt chunks: $Path" }
            if ($chunkSize -lt 16) { Fail "WAV fmt chunk is too short: $Path" }
            $fmt = [pscustomobject]@{
                Format = [BitConverter]::ToUInt16($bytes, [int]$chunkStart)
                Channels = [BitConverter]::ToUInt16($bytes, [int]$chunkStart + 2)
                SampleRate = [int](Get-UInt32 $bytes ([int]$chunkStart + 4))
                ByteRate = [uint64](Get-UInt32 $bytes ([int]$chunkStart + 8))
                BlockAlign = [BitConverter]::ToUInt16($bytes, [int]$chunkStart + 12)
                Bits = [BitConverter]::ToUInt16($bytes, [int]$chunkStart + 14)
            }
        } elseif ($chunkId -eq 'data') {
            if ($dataOffset -ge 0) { Fail "WAV has duplicate data chunks: $Path" }
            if ($chunkSize -le 0 -or ($chunkSize % 2) -ne 0) { Fail "WAV data is empty or not PCM16-aligned: $Path" }
            $dataOffset = $chunkStart
            $dataSize = $chunkSize
        }
        $offset = $chunkEnd + ($chunkSize % 2)
    }
    if ($null -eq $fmt) { Fail "WAV is missing its fmt chunk: $Path" }
    if ($dataOffset -lt 0) { Fail "WAV is missing its data chunk: $Path" }
    if ($fmt.Format -ne 1 -or $fmt.Channels -ne 1 -or $fmt.Bits -ne 16) {
        Fail "WAV must be PCM16 mono: $Path (format=$($fmt.Format), channels=$($fmt.Channels), bits=$($fmt.Bits))"
    }
    if ($RequiredRates -notcontains $fmt.SampleRate) { Fail "WAV sample rate must be 16000 or 24000 Hz: $Path" }
    if ($fmt.BlockAlign -ne 2 -or $fmt.ByteRate -ne ($fmt.SampleRate * 2)) { Fail "WAV byte rate/alignment is invalid: $Path" }

    $sampleCount = [int]($dataSize / 2)
    $samples = New-Object -TypeName 'System.Int16[]' -ArgumentList $sampleCount
    if ([BitConverter]::IsLittleEndian) { [Buffer]::BlockCopy($bytes, [int]$dataOffset, $samples, 0, [int]$dataSize) }
    else { for ($index = 0; $index -lt $sampleCount; $index++) { $samples[$index] = [BitConverter]::ToInt16($bytes, [int]$dataOffset + ($index * 2)) } }
    return [pscustomobject]@{ Bytes = $bytes; Samples = $samples; SampleRate = $fmt.SampleRate }
}
function Get-Rms([int16[]]$Samples) {
    if ($Samples.Count -eq 0) { Fail 'cannot measure RMS for empty samples' }
    $sum = 0.0; foreach ($sample in $Samples) { $value = [double]$sample; $sum += $value * $value }
    return [math]::Sqrt($sum / $Samples.Count)
}
function Get-RmsRange([int16[]]$Samples, [int]$Start, [int]$Count, [int]$Stride = 1) {
    if ($Count -le 0 -or $Start -lt 0 -or ($Start + $Count) -gt $Samples.Count) { Fail 'invalid RMS range' }
    $sum = 0.0
    $observed = 0
    for ($index = $Start; $index -lt ($Start + $Count); $index += $Stride) { $value = [double]$Samples[$index]; $sum += $value * $value; $observed++ }
    return [math]::Sqrt($sum / $observed)
}
function Boost-Speech([int16[]]$Samples, [double]$Rms) {
    if ($Rms -le 0) { Fail 'TTS returned digital silence for a speech source' }
    if ($Rms -ge $VadThreshold) { return $Samples }
    $gain = ($VadThreshold * 2.0) / $Rms
    $result = New-Object -TypeName 'System.Int16[]' -ArgumentList $Samples.Count
    for ($index = 0; $index -lt $Samples.Count; $index++) { $result[$index] = Clamp-Int16 ([double]$Samples[$index] * $gain) }
    if ((Get-Rms $result) -lt $VadThreshold) { Fail "TTS speech remained below $VadThreshold RMS after deterministic gain" }
    return $result
}
function Get-NonZeroCount([int16[]]$Samples, [int]$Start, [int]$Count) {
    $end = [math]::Min($Samples.Count, $Start + $Count); $found = 0
    for ($index = [math]::Max(0, $Start); $index -lt $end; $index++) {
        if ($Samples[$index] -ne 0) { $found = $found + 1 }
    }
    return $found
}
function Clamp-Int16([double]$Value) {
    if ($Value -gt 32767) { return [int16]32767 }; if ($Value -lt -32768) { return [int16]-32768 }
    return [int16][math]::Round($Value, 0, [MidpointRounding]::AwayFromZero)
}
function Take-Samples([int16[]]$Samples, [int]$Count, [string]$Label) {
    if ($Count -le 0 -or $Count -gt $Samples.Count) { Fail "$Label does not contain $Count samples" }; $result = New-Object -TypeName 'System.Int16[]' -ArgumentList $Count
    [Array]::Copy($Samples, $result, $Count)
    return $result
}
function Find-LoudestSlice([int16[]]$Samples, [int]$Rate, [int]$Count) {
    if ($Count -le 0 -or $Count -ge $Samples.Count) { Fail 'truncated slice must be shorter than its source' }
    $latestStart = $Samples.Count - $Count - [int]($Rate * 0.25)
    $step = [math]::Max(1, [int]($Rate * 1.0))
    $tailCount = [int]($Rate * 0.1)
    $bestStart = -1
    $bestRms = -1.0
    for ($start = 0; $start -le $latestStart; $start += $step) {
        $tail = Get-NonZeroCount $Samples ($start + $Count - $tailCount) $tailCount
        if ($tail -lt 10) { continue }
        $rms = Get-RmsRange $Samples $start $Count 64
        if ($rms -gt $bestRms) { $bestRms = $rms; $bestStart = $start }
    }
    if ($bestStart -lt 0) { Fail 'could not find an audible truncated source window' }
    $result = New-Object -TypeName 'System.Int16[]' -ArgumentList $Count
    [Array]::Copy($Samples, $bestStart, $result, 0, $Count)
    return $result
}
function Join-Samples([object[]]$Parts) {
    $result = [Collections.Generic.List[int16]]::new()
    foreach ($part in $Parts) { foreach ($sample in [int16[]]$part) { [void]$result.Add($sample) } }
    return $result.ToArray()
}
function New-Silence([int]$Count) { return New-Object -TypeName 'System.Int16[]' -ArgumentList $Count }
function New-Noise([int]$Count) {
    $result = New-Object -TypeName 'System.Int16[]' -ArgumentList $Count
    [int64]$state = 17
    for ($index = 0; $index -lt $Count; $index++) {
        $state = ($state * 1103515245 + 12345) % 2147483648
        $result[$index] = [int16]([int]($state % 161) - 80)
    }
    return $result
}
function Mix-Samples([int16[]]$Left, [int16[]]$Right, [int]$Offset) {
    $count = [math]::Max($Left.Count, $Offset + $Right.Count)
    $result = New-Object -TypeName 'System.Int16[]' -ArgumentList $count
    for ($index = 0; $index -lt $count; $index++) {
        $sum = 0.0
        if ($index -lt $Left.Count) { $sum += $Left[$index] }
        $rightIndex = $index - $Offset
        if ($rightIndex -ge 0 -and $rightIndex -lt $Right.Count) { $sum += $Right[$rightIndex] }
        $result[$index] = Clamp-Int16 $sum
    }
    return $result
}
function Resample([int16[]]$Samples, [int]$FromRate, [int]$ToRate) {
    if ($FromRate -eq $ToRate) { return Take-Samples $Samples $Samples.Count 'resampler input' }
    $count = [int][math]::Floor(($Samples.Count * [double]$ToRate) / $FromRate)
    if ($count -le 0) { Fail "resampling produced no samples ($FromRate to $ToRate)" }
    $result = New-Object -TypeName 'System.Int16[]' -ArgumentList $count
    for ($index = 0; $index -lt $count; $index++) {
        $position = $index * ([double]$FromRate / $ToRate)
        $left = [int][math]::Floor($position)
        $fraction = $position - $left
        if ($left -ge ($Samples.Count - 1)) { $value = $Samples[$Samples.Count - 1] }
        else { $value = $Samples[$left] + (($Samples[$left + 1] - $Samples[$left]) * $fraction) }
        $result[$index] = Clamp-Int16 $value
    }
    return $result
}
function Write-Pcm16Wav([string]$Path, [int]$SampleRate, [int16[]]$Samples) {
    if ($Samples.Count -eq 0) { Fail "cannot write empty WAV: $Path" }
    $dataSize = [int64]$Samples.Count * 2
    if ($dataSize -gt [uint32]::MaxValue - 36) { Fail "WAV is too large: $Path" }
    $stream = [IO.MemoryStream]::new()
    $writer = [IO.BinaryWriter]::new($stream)
    try {
        $writer.Write([Text.Encoding]::ASCII.GetBytes('RIFF')); $writer.Write([int32]($dataSize + 36)); $writer.Write([Text.Encoding]::ASCII.GetBytes('WAVE'))
        $writer.Write([Text.Encoding]::ASCII.GetBytes('fmt ')); $writer.Write([int32]16); $writer.Write([int16]1); $writer.Write([int16]1)
        $writer.Write([int32]$SampleRate); $writer.Write([int32]($SampleRate * 2)); $writer.Write([int16]2); $writer.Write([int16]16)
        $writer.Write([Text.Encoding]::ASCII.GetBytes('data')); $writer.Write([int32]$dataSize)
        $data = New-Object byte[] ([int]$dataSize)
        [Buffer]::BlockCopy($Samples, 0, $data, 0, [int]$dataSize)
        $writer.Write($data); $writer.Flush(); [IO.File]::WriteAllBytes($Path, $stream.ToArray())
    } finally {
        $writer.Dispose(); $stream.Dispose()
    }
}

function Invoke-Tts([string]$Id, [string]$Text, [string]$Path) {
    $arguments = @('-m', $ModelDirectory, '-t', $Text, '-o', $Path, '-l', 'en', '--temperature', '0', '--top-k', '1', '--top-p', '1', '--max-tokens', '256', '--repetition-penalty', '1', '-j', '4')
    if ($null -ne $ReferenceAudioPath) { $arguments = @('-m', $ModelDirectory, '-r', $ReferenceAudioPath, '-t', $Text, '-o', $Path, '-l', 'en', '--temperature', '0', '--top-k', '1', '--top-p', '1', '--max-tokens', '256', '--repetition-penalty', '1', '-j', '4') }
    if ([IO.Path]::GetExtension($TtsBinary) -ieq '.ps1') { $output = @(& pwsh -NoProfile -File $TtsBinary @arguments 2>&1) } else { $output = @(& $TtsBinary @arguments 2>&1) }
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        $detail = (($output | ForEach-Object { "$_" }) -join [Environment]::NewLine)
        if ($detail.Length -gt 1200) { $detail = $detail.Substring($detail.Length - 1200) }
        Fail "TTS failed for $Id with exit code $exitCode. $detail"
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { Fail "TTS produced no WAV for ${Id}: $Path" }
    $wav = Read-Pcm16Wav $Path; $rms = Get-Rms $wav.Samples; $samples = Boost-Speech $wav.Samples $rms
    return [pscustomobject]@{ Bytes = $wav.Bytes; Samples = $samples; SampleRate = $wav.SampleRate }
}
function Assert-ClassShape($Definition, [int16[]]$Samples, [int]$Rate, [double]$Rms) {
    if ($Definition.Class -eq 'silence') {
        if ((Get-NonZeroCount $Samples 0 $Samples.Count) -ne 0 -or $Rms -ne 0) { Fail "$($Definition.Class) is not digital silence" }
    } elseif ($Definition.Class -eq 'noise') {
        if ($Rms -le 0 -or $Rms -ge $VadThreshold) { Fail "noise RMS is $Rms; want 0 < RMS < $VadThreshold" }
    } elseif ($Rms -lt $VadThreshold) {
        Fail "$($Definition.Class) RMS is $Rms; want RMS >= $VadThreshold"
    }
    if ($Definition.Class -eq 'utt_long' -and (($Samples.Count / [double]$Rate) -le 10.0)) { Fail 'utt_long must exceed 10 seconds' }
    if ($Definition.Class -eq 'overlap') {
        $offset = [int][math]::Round($Rate * 0.75)
        if ((Get-NonZeroCount $Samples 0 $offset) -lt 10 -or (Get-NonZeroCount $Samples $offset ([int]($Rate * 0.75))) -lt 10) { Fail "overlap lacks two active regions at ${Rate}Hz" }
    }
    if ($Definition.Class -eq 'multi_utt') {
        if ($null -eq $Definition.FirstUtteranceSamples -or $null -eq $Definition.GapSamples) { Fail "multi_utt definition is missing its audible boundary at ${Rate}Hz" }
        $gapStart = [int]$Definition.FirstUtteranceSamples
        $gapCount = [int]$Definition.GapSamples
        if ($gapStart -le 0 -or $gapCount -le 0) { Fail "multi_utt has invalid audible boundary at ${Rate}Hz" }
        if ((Get-NonZeroCount $Samples ($gapStart + [int]($gapCount * 0.1)) ([int]($gapCount * 0.8))) -ne 0) { Fail "multi_utt lacks its silent separation at ${Rate}Hz" }
        if ((Get-NonZeroCount $Samples 0 $gapStart) -lt 10 -or (Get-NonZeroCount $Samples ($gapStart + $gapCount) ([int]($Rate * 0.75))) -lt 10) { Fail "multi_utt lacks two audible utterances at ${Rate}Hz" }
    }
    if ($Definition.Class -eq 'truncated') {
        $tailCount = Get-NonZeroCount $Samples ([math]::Max(0, $Samples.Count - [int]($Rate * 0.1))) ([int]($Rate * 0.1))
        if ($tailCount -lt 10) { Fail 'truncated does not end in audible mid-utterance data' }
    }
}
function New-ExpectedFiles($Definitions, [int]$SourceRate, [string]$Staging) {
    $expected = [Collections.Generic.List[object]]::new()
    foreach ($definition in $Definitions) {
        foreach ($rate in $RequiredRates) {
            $rateLabel = if ($rate -eq 16000) { '16k' } else { '24k' }
            $id = "$($definition.Id)_$rateLabel"
            $samples = Resample $definition.Samples $SourceRate $rate
            $firstUtteranceSamples = $null
            $gapSamples = $null
            if ($null -ne $definition.FirstUtteranceSamples -and $null -ne $definition.GapSamples) {
                $firstBoundary = [int][math]::Floor($definition.FirstUtteranceSamples * ([double]$rate / $SourceRate))
                $gapBoundary = [int][math]::Floor(($definition.FirstUtteranceSamples + $definition.GapSamples) * ([double]$rate / $SourceRate))
                $firstUtteranceSamples = $firstBoundary
                $gapSamples = $gapBoundary - $firstBoundary
            }
            $path = Join-Path $Staging "$id.wav"
            Write-Pcm16Wav $path $rate $samples
            [void]$expected.Add([pscustomobject]@{ Id = $id; Path = "$id.wav"; Class = $definition.Class; Source = $definition.Source; Structure = $definition.Structure; Voice = $definition.Voice; Samples = $samples; Rate = $rate; FirstUtteranceSamples = $firstUtteranceSamples; GapSamples = $gapSamples })
        }
    }
    return @($expected | Sort-Object Path)
}
function New-ManifestEntries($Expected, [string]$Staging) {
    $rootFiles = @(Get-ChildItem -LiteralPath $Staging -File)
    if (@(Get-ChildItem -LiteralPath $Staging -Directory).Count -ne 0) { Fail 'staging output contains an unexpected directory' }
    if ($rootFiles.Count -ne $Expected.Count) { Fail "staging contains $($rootFiles.Count) files before manifest; want $($Expected.Count)" }
    $entries = [Collections.Generic.List[object]]::new()
    foreach ($item in $Expected) {
        $path = Join-Path $Staging $item.Path
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { Fail "missing expected WAV: $($item.Path)" }
        $wav = Read-Pcm16Wav $path
        if ($wav.SampleRate -ne $item.Rate -or $wav.Samples.Count -ne $item.Samples.Count) { Fail "final WAV facts disagree for $($item.Id)" }
        $rms = Get-Rms $wav.Samples
        Assert-ClassShape $item $wav.Samples $wav.SampleRate $rms
        [void]$entries.Add([pscustomobject][ordered]@{
            id = $item.Id; path = $item.Path; class = $item.Class; source = $item.Source; voice = $item.Voice; structure = $item.Structure
            format = [ordered]@{ container = 'wav'; encoding = 'PCM'; sample_format = 's16le'; channels = 1; bits_per_sample = 16 }; sample_rate_hz = $wav.SampleRate; channels = 1; bits_per_sample = 16; sample_count = $wav.Samples.Count
            duration_seconds = [double]($wav.Samples.Count / [double]$wav.SampleRate); rms_energy = [double]$rms; byte_size = (Get-Item -LiteralPath $path).Length; sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        })
    }
    return @($entries)
}
function ConvertTo-CanonicalJson($Value) {
    $json = $Value | ConvertTo-Json -Depth 10
    return $json.Replace("`r`n", "`n").Replace("`r", "`n")
}
function Write-Manifest([string]$Staging, $Entries) {
    $manifestPath = Join-Path $Staging 'manifest.json'
    $manifest = [ordered]@{ schema_version = 1; corpus_byte_total = [int64]0; vad_threshold_rms = [double]$VadThreshold; classes = $RequiredClasses; sample_rates_hz = $RequiredRates; files = $Entries }
    $target = [int64]0
    for ($attempt = 0; $attempt -lt 8; $attempt++) {
        $manifest.corpus_byte_total = $target
        $json = ConvertTo-CanonicalJson $manifest
        [IO.File]::WriteAllText($manifestPath, $json, [Text.UTF8Encoding]::new($false))
        $total = [int64]0
        foreach ($file in @(Get-ChildItem -LiteralPath $Staging -File)) { $total += $file.Length }
        if ($total -eq $target) { return $total }
        $target = $total
    }
    Fail 'manifest corpus_byte_total did not reach a stable fixed point'
}
function Assert-Manifest([string]$Staging, $Expected) {
    $manifestPath = Join-Path $Staging 'manifest.json'
    Assert-FileInput $manifestPath 'manifest'
    try { $manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json -Depth 10 } catch { Fail "manifest is not valid JSON: $($_.Exception.Message)" }
    if ($manifest.schema_version -ne 1 -or $manifest.vad_threshold_rms -ne $VadThreshold) { Fail 'manifest schema or VAD threshold is invalid' }
    if (@($manifest.files).Count -ne $Expected.Count) { Fail 'manifest does not contain exactly 16 entries' }
    $byPath = @{}
    foreach ($item in $Expected) { $byPath[$item.Path] = $item }
    $seen = @{}
    foreach ($entry in @($manifest.files)) {
        if (-not $byPath.ContainsKey([string]$entry.path)) { Fail "manifest contains unexpected path: $($entry.path)" }
        if ($seen.ContainsKey([string]$entry.path)) { Fail "manifest contains duplicate path: $($entry.path)" }
        $seen[[string]$entry.path] = $true
        $expectedItem = $byPath[[string]$entry.path]
        if ([string]$entry.id -ne $expectedItem.Id -or [string]$entry.class -ne $expectedItem.Class -or [int]$entry.sample_rate_hz -ne $expectedItem.Rate) { Fail "manifest identity mismatch for $($entry.path)" }
        $path = Join-Path $Staging ([string]$entry.path)
        $wav = Read-Pcm16Wav $path
        $rms = Get-Rms $wav.Samples
        Assert-ClassShape $expectedItem $wav.Samples $wav.SampleRate $rms
        if ($entry.sample_count -ne $wav.Samples.Count -or [math]::Abs([double]$entry.duration_seconds - ($wav.Samples.Count / [double]$wav.SampleRate)) -gt 0.000000001) { Fail "manifest duration/sample count mismatch for $($entry.path)" }
        if ([math]::Abs([double]$entry.rms_energy - $rms) -gt 0.000001) { Fail "manifest RMS mismatch for $($entry.path)" }
        if ($entry.byte_size -ne (Get-Item -LiteralPath $path).Length) { Fail "manifest byte size mismatch for $($entry.path)" }
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        if ([string]$entry.sha256 -ne $hash) { Fail "manifest SHA-256 mismatch for $($entry.path)" }
        if ($entry.format.container -ne 'wav' -or $entry.format.sample_format -ne 's16le' -or $entry.format.channels -ne 1 -or $entry.format.bits_per_sample -ne 16 -or $entry.channels -ne 1 -or $entry.bits_per_sample -ne 16) { Fail "manifest format mismatch for $($entry.path)" }
    }
    if (@($seen.Keys).Count -ne $Expected.Count) { Fail "manifest is missing an expected path (seen=$(@($seen.Keys).Count), expected=$($Expected.Count))" }
    $total = [int64]0
    foreach ($file in @(Get-ChildItem -LiteralPath $Staging -File)) { $total += $file.Length }
    if ([int64]$manifest.corpus_byte_total -ne $total) { Fail "manifest corpus_byte_total is $($manifest.corpus_byte_total); actual total is $total" }
    if ($total -gt $MaxCorpusBytes) { Fail "corpus is $total bytes; maximum is $MaxCorpusBytes" }
    $rootFiles = @(Get-ChildItem -LiteralPath $Staging -File)
    if ($rootFiles.Count -ne ($Expected.Count + 1) -or @(Get-ChildItem -LiteralPath $Staging -Directory).Count -ne 0) { Fail 'published staging contains unexpected files or directories' }
}
function Publish-Corpus([string]$Staging, [string]$Output) {
    $backup = "$Output.previous-$([guid]::NewGuid().ToString('N'))"
    $hadPrevious = Test-Path -LiteralPath $Output
    try {
        if ($hadPrevious) { [void](Move-Item -LiteralPath $Output -Destination $backup -Force -ErrorAction Stop) }
        [void](Move-Item -LiteralPath $Staging -Destination $Output -Force -ErrorAction Stop)
        if ($hadPrevious) { Remove-Item -LiteralPath $backup -Recurse -Force -ErrorAction Stop }
    } catch {
        if (Test-Path -LiteralPath $Output) { Remove-Item -LiteralPath $Output -Recurse -Force -ErrorAction SilentlyContinue }
        if ($hadPrevious -and (Test-Path -LiteralPath $backup)) { [void](Move-Item -LiteralPath $backup -Destination $Output -Force -ErrorAction SilentlyContinue) }
        throw
    }
}
$workRoot = $null
$staging = $null
$exitCode = 0
try {
    $TtsBinary = Resolve-InputPath $TtsBinary 'TTS binary'
    Assert-FileInput $TtsBinary 'TTS binary'
    $ModelDirectory = Resolve-InputPath $ModelDirectory 'model directory'
    Assert-ModelDirectory $ModelDirectory
    $outputPath = Resolve-InputPath $OutputDirectory 'output directory'
    if ([IO.Path]::GetPathRoot($outputPath) -eq $outputPath) { Fail 'output directory must be a named directory, not a filesystem root' }
    $outputParent = Split-Path -Parent $outputPath
    if ([string]::IsNullOrWhiteSpace((Split-Path -Leaf $outputPath))) { Fail 'output directory must have a name' }
    New-Item -ItemType Directory -Path $outputParent -Force | Out-Null
    if (Test-Path -LiteralPath $outputPath -PathType Leaf) { Fail "output path is a file: $outputPath" }
    if ([string]::IsNullOrWhiteSpace($Voice)) { Fail 'voice is required' }
    if (-not [string]::IsNullOrWhiteSpace($ReferenceAudio)) {
        $ReferenceAudioPath = Resolve-InputPath $ReferenceAudio 'reference audio'
        Assert-FileInput $ReferenceAudioPath 'reference audio'
    } elseif ($Voice -match '(?i)\.wav$' -or [IO.Path]::IsPathRooted($Voice) -or $Voice -match '[\\/]') {
        $candidate = Resolve-InputPath $Voice 'voice reference audio'
        Assert-FileInput $candidate 'voice reference audio'
        $ReferenceAudioPath = $candidate
    }
    $workRoot = Join-Path ([IO.Path]::GetTempPath()) "audio-corpus-$([guid]::NewGuid().ToString('N'))"
    $sourceRoot = Join-Path $workRoot 'sources'
    $staging = Join-Path $outputParent ".$([IO.Path]::GetFileName($outputPath)).staging-$([guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $sourceRoot,$staging -Force | Out-Null
    $shortText = 'The timer is ready for the next step.'
    $longText = 'This deterministic offline recording contains a complete long utterance for speech to speech tests and remains stable across regeneration.'
    $toolText = 'Open the calendar.'
    $shortWav = Invoke-Tts 'utt_short_source' $shortText (Join-Path $sourceRoot 'short.wav')
    $longWav = Invoke-Tts 'utt_long_source' $longText (Join-Path $sourceRoot 'long.wav')
    $toolWav = Invoke-Tts 'tool_request_source' $toolText (Join-Path $sourceRoot 'tool.wav')
    if ($shortWav.SampleRate -ne $longWav.SampleRate -or $shortWav.SampleRate -ne $toolWav.SampleRate) { Fail 'TTS sources have inconsistent sample rates' }
    $sourceRate = $shortWav.SampleRate
    $shortSamples = $shortWav.Samples
    $truncatedSamples = Find-LoudestSlice $longWav.Samples $sourceRate ([int]($sourceRate * 2.75))
    $definitions = @(
        [pscustomobject]@{ Id = 'utt_short'; Class = 'utt_short'; Source = $shortText; Structure = 'complete synthesized short utterance retained without windowing'; Voice = $Voice; Samples = $shortSamples }
        [pscustomobject]@{ Id = 'utt_long'; Class = 'utt_long'; Source = $longText; Structure = 'complete utterance longer than ten seconds'; Voice = $Voice; Samples = $longWav.Samples }
        [pscustomobject]@{ Id = 'silence'; Class = 'silence'; Source = 'digital silence: every PCM16 sample is zero'; Structure = 'digital silence'; Voice = $Voice; Samples = New-Silence ([int]($sourceRate * 1.0)) }
        [pscustomobject]@{ Id = 'noise'; Class = 'noise'; Source = 'deterministic low-amplitude noise with fixed seed 17'; Structure = 'low-energy deterministic noise'; Voice = $Voice; Samples = New-Noise ([int]($sourceRate * 1.0)) }
        [pscustomobject]@{ Id = 'overlap'; Class = 'overlap'; Source = "$shortText / $toolText"; Structure = 'two audible clips mixed with 0.75 seconds of simultaneous activity'; Voice = $Voice; Samples = Mix-Samples $shortSamples $toolWav.Samples ([int]($sourceRate * 0.75)) }
        [pscustomobject]@{ Id = 'multi_utt'; Class = 'multi_utt'; Source = "$shortText / $toolText"; Structure = 'two audible utterances separated by 0.6 seconds of silence'; Voice = $Voice; Samples = Join-Samples @($shortSamples, (New-Silence ([int]($sourceRate * 0.6))), $toolWav.Samples); FirstUtteranceSamples = $shortSamples.Count; GapSamples = [int]($sourceRate * 0.6) }
        [pscustomobject]@{ Id = 'truncated'; Class = 'truncated'; Source = "cut before the natural end of: $longText"; Structure = 'loudest long-source window cut at 2.75 seconds'; Voice = $Voice; Samples = $truncatedSamples }
        [pscustomobject]@{ Id = 'tool_request'; Class = 'tool_request'; Source = $toolText; Structure = 'audible concrete calendar reminder request'; Voice = $Voice; Samples = $toolWav.Samples }
    )
    $expected = New-ExpectedFiles $definitions $sourceRate $staging
    $entries = New-ManifestEntries $expected $staging
    [void](Write-Manifest $staging $entries)
    Assert-Manifest $staging $expected
    Publish-Corpus $staging $outputPath
    Write-Host "AUDIO_CORPUS_PASS files=16 output=$outputPath bytes=$((Get-Item -LiteralPath (Join-Path $outputPath 'manifest.json')).Length)"
} catch {
    Write-Error $_.Exception.Message
    $exitCode = 1
} finally {
    if ($null -ne $workRoot -and (Test-Path -LiteralPath $workRoot)) { Remove-Item -LiteralPath $workRoot -Recurse -Force -ErrorAction SilentlyContinue }
    if ($null -ne $staging -and (Test-Path -LiteralPath $staging)) { Remove-Item -LiteralPath $staging -Recurse -Force -ErrorAction SilentlyContinue }
}
exit $exitCode

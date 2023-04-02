$sourceDir = "C:\path\to\sourceDir"
$destinationParentDir = "C:\path\to\destDir"

# 001 から 100 までのサブフォルダを作成
for ($i = 1; $i -le 100; $i++) {
    $newFolderPath = Join-Path $destinationParentDir ([string]::Format("{0:D3}", $i))
    if (!(Test-Path $newFolderPath)) {
        New-Item -ItemType Directory -Path $newFolderPath | Out-Null
    }
}

# サブフォルダを取得
Get-ChildItem -Path $sourceDir -Directory
$subDirs = Get-ChildItem -Path $sourceDir -Directory

# サブフォルダの総数を取得
$subDirsCount = $subDirs.Count

# サブフォルダを適切に分散するためのインデックス
$dirIndex = 0

# サブフォルダを分割先のフォルダにコピー
foreach ($dir in $subDirs) {
    $destinationPath = Join-Path $destinationParentDir ([string]::Format("{0:D3}", ($dirIndex % 100) + 1))
    $destinationSubFolderPath = Join-Path $destinationPath $dir.Name

    # 同じ名前のサブフォルダが存在する場合はログ出力してスキップ
    if (Test-Path $destinationSubFolderPath) {
        Write-Host "Skipping: A directory with the same name already exists in $($destinationPath) - $($dir.Name)"
    } else {
        Write-Host "Starting move of $($dir.Name)"
        Move-Item -Path $dir.FullName -Destination $destinationPath
        Write-Host "Finished moving $($dir.Name)"
    }

    $dirIndex++
}

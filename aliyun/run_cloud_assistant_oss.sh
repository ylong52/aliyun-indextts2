#!/bin/bash
# 编译并运行 cloud_assistant_oss.go

echo "Building cloud_assistant_oss..."
go build -tags "cloud_assistant_oss" -o cloud_assistant_oss cloud_assistant.go cloud_assistant_oss.go instance_manager.go
if [ $? -ne 0 ]; then
    echo "Build failed!"
    exit 1
fi

echo "Build successful!"
echo ""
echo "Running cloud_assistant_oss..."
echo ""
./cloud_assistant_oss


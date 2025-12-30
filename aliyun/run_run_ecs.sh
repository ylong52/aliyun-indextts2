#!/bin/bash
# 编译并运行 run_ecs.go

echo "Building run_ecs..."
go build -tags "!release_ecs" -o run_ecs run_ecs.go instance_manager.go
if [ $? -ne 0 ]; then
    echo "Build failed!"
    exit 1
fi

echo "Build successful!"
echo ""
echo "Running run_ecs..."
echo ""
./run_ecs


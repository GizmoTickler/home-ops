#!/bin/bash
# Test script to verify Cloudflare Worker deployment and check for domain leakage

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Get worker URL from user
read -p "Enter your worker URL (e.g., https://kromgo-proxy.username.workers.dev): " WORKER_URL
read -p "Enter your secret domain to check for leaks (e.g., example.com): " SECRET_DOMAIN

echo ""
echo "Testing Cloudflare Worker: $WORKER_URL"
echo "Checking for domain leakage: $SECRET_DOMAIN"
echo ""

# Test metrics
METRICS=(
  "talos_version"
  "kubernetes_version"
  "flux_version"
  "cluster_node_count"
  "cluster_pod_count"
  "cluster_cpu_usage"
  "cluster_memory_usage"
  "cluster_age_days"
  "cluster_uptime_days"
  "cluster_alert_count"
)

PASSED=0
FAILED=0
LEAKED=0

echo "=========================================="
echo "Testing Metric Endpoints"
echo "=========================================="

for metric in "${METRICS[@]}"; do
  echo -n "Testing $metric... "

  # Fetch the metric with verbose output to capture headers
  RESPONSE=$(curl -s -w "\n%{http_code}" "${WORKER_URL}/${metric}")
  HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
  BODY=$(echo "$RESPONSE" | head -n-1)

  if [ "$HTTP_CODE" = "200" ]; then
    echo -e "${GREEN}✓ PASS${NC} (HTTP $HTTP_CODE)"
    ((PASSED++))

    # Check for domain leakage in response body
    if echo "$BODY" | grep -qi "$SECRET_DOMAIN"; then
      echo -e "  ${RED}⚠ DOMAIN LEAKED IN RESPONSE!${NC}"
      echo "  Found: $(echo "$BODY" | grep -i "$SECRET_DOMAIN")"
      ((LEAKED++))
    fi

    # Validate JSON structure
    if echo "$BODY" | jq empty 2>/dev/null; then
      echo "  JSON: Valid"
    else
      echo -e "  ${YELLOW}JSON: Invalid${NC}"
    fi
  else
    echo -e "${RED}✗ FAIL${NC} (HTTP $HTTP_CODE)"
    ((FAILED++))
    echo "  Response: $BODY"
  fi
done

echo ""
echo "=========================================="
echo "Testing Invalid Endpoints"
echo "=========================================="

# Test invalid metric (should return 404)
echo -n "Testing invalid metric (should 404)... "
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${WORKER_URL}/invalid_metric_12345")
if [ "$HTTP_CODE" = "404" ]; then
  echo -e "${GREEN}✓ PASS${NC} (HTTP $HTTP_CODE)"
  ((PASSED++))
else
  echo -e "${RED}✗ FAIL${NC} (HTTP $HTTP_CODE - expected 404)"
  ((FAILED++))
fi

# Test path traversal (should return 404)
echo -n "Testing path traversal (should 404)... "
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${WORKER_URL}/../etc/passwd")
if [ "$HTTP_CODE" = "404" ]; then
  echo -e "${GREEN}✓ PASS${NC} (HTTP $HTTP_CODE)"
  ((PASSED++))
else
  echo -e "${RED}✗ FAIL${NC} (HTTP $HTTP_CODE - expected 404)"
  ((FAILED++))
fi

# Test POST method (should return 405)
echo -n "Testing POST method (should 405)... "
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${WORKER_URL}/talos_version")
if [ "$HTTP_CODE" = "405" ]; then
  echo -e "${GREEN}✓ PASS${NC} (HTTP $HTTP_CODE)"
  ((PASSED++))
else
  echo -e "${RED}✗ FAIL${NC} (HTTP $HTTP_CODE - expected 405)"
  ((FAILED++))
fi

echo ""
echo "=========================================="
echo "Checking for Domain Leakage in Headers"
echo "=========================================="

# Check headers for domain leakage
echo -n "Checking response headers... "
HEADERS=$(curl -s -I "${WORKER_URL}/talos_version")
if echo "$HEADERS" | grep -qi "$SECRET_DOMAIN"; then
  echo -e "${RED}✗ DOMAIN LEAKED IN HEADERS!${NC}"
  echo "$HEADERS" | grep -i "$SECRET_DOMAIN"
  ((LEAKED++))
else
  echo -e "${GREEN}✓ No leakage detected${NC}"
fi

echo ""
echo "=========================================="
echo "Test Summary"
echo "=========================================="
echo "Passed: $PASSED"
echo "Failed: $FAILED"
echo "Domain Leaks: $LEAKED"
echo ""

if [ $FAILED -eq 0 ] && [ $LEAKED -eq 0 ]; then
  echo -e "${GREEN}✓ All tests passed! Worker is secure and functional.${NC}"
  exit 0
else
  echo -e "${RED}✗ Some tests failed. Review the output above.${NC}"
  exit 1
fi

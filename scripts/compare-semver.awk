function compare_number(left, right) {
  left += 0
  right += 0
  return left < right ? -1 : left > right ? 1 : 0
}

function is_numeric(value) {
  return value ~ /^(0|[1-9][0-9]*)$/
}

function compare_identifier(left, right, left_numeric, right_numeric) {
  left_numeric = is_numeric(left)
  right_numeric = is_numeric(right)
  if (left_numeric && right_numeric) {
    return compare_number(left, right)
  }
  if (left_numeric != right_numeric) {
    return left_numeric ? -1 : 1
  }
  return left < right ? -1 : left > right ? 1 : 0
}

function compare_version(left, right, left_core, right_core, left_pre, right_pre,
                         dash, count, part, result, left_count, right_count) {
  sub(/\+.*/, "", left)
  sub(/\+.*/, "", right)

  dash = index(left, "-")
  left_core = dash ? substr(left, 1, dash - 1) : left
  left_pre = dash ? substr(left, dash + 1) : ""
  dash = index(right, "-")
  right_core = dash ? substr(right, 1, dash - 1) : right
  right_pre = dash ? substr(right, dash + 1) : ""

  split(left_core, left_parts, ".")
  split(right_core, right_parts, ".")
  for (part = 1; part <= 3; part++) {
    result = compare_number(left_parts[part], right_parts[part])
    if (result) {
      return result
    }
  }

  if (left_pre == right_pre) {
    return 0
  }
  if (left_pre == "") {
    return 1
  }
  if (right_pre == "") {
    return -1
  }

  delete left_parts
  delete right_parts
  left_count = split(left_pre, left_parts, ".")
  right_count = split(right_pre, right_parts, ".")
  count = left_count > right_count ? left_count : right_count
  for (part = 1; part <= count; part++) {
    if (part > left_count) {
      return -1
    }
    if (part > right_count) {
      return 1
    }
    result = compare_identifier(left_parts[part], right_parts[part])
    if (result) {
      return result
    }
  }
  return 0
}

BEGIN {
  if (ARGC != 3) {
    print "usage: awk -f compare-semver.awk LEFT RIGHT" > "/dev/stderr"
    exit 2
  }
  print compare_version(ARGV[1], ARGV[2])
  exit
}

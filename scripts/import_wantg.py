#!/usr/bin/env python3
"""Validate user-provided wantg CSVs and replace the bundled dataset.

Input files are read only. All validation finishes before output files change.
Prices are preserved as decimal strings; K/D indicators are not imported.
"""

import argparse
import csv
import hashlib
import json
import re
from datetime import date
from decimal import Decimal, InvalidOperation
from pathlib import Path


def read_rows(path, expected_header):
    with path.open(encoding="utf-8-sig", newline="") as handle:
        reader = csv.DictReader(handle)
        if reader.fieldnames != expected_header:
            raise ValueError(f"Unexpected CSV header: {path.name}")
        yield from reader


def import_dataset(input_dir, output_dir):
    group_files = sorted(input_dir.glob("group_*_stock_name.mapping.csv"))
    price_files = sorted(input_dir.glob("*.d.csv"))
    if not group_files or not price_files:
        raise ValueError("Missing group mapping or daily price CSVs")

    names, memberships = {}, set()
    for path in group_files:
        group = path.name.removeprefix("group_").removesuffix("_stock_name.mapping.csv")
        if not group:
            raise ValueError(f"Missing group name: {path.name}")
        for row in read_rows(path, ["stock_id", "stock_name"]):
            code, name = row["stock_id"].strip(), row["stock_name"].strip()
            if not code or not name or (code in names and names[code] != name):
                raise ValueError(f"Invalid/conflicting stock mapping: {path.name}")
            names[code] = name
            memberships.add((group, code))
    if len(set(names.values())) != len(names):
        raise ValueError("Stock names must be unique")

    prices, seen, priced_codes = [], set(), set()
    for path in price_files:
        code = path.name.removesuffix(".d.csv")
        if code not in names:
            raise ValueError(f"Missing stock name: {code}")
        for row in read_rows(path, ["time", "open", "close", "high", "low", "K", "D"]):
            day = row["time"]
            if date.fromisoformat(day).isoformat() != day:
                raise ValueError(f"Invalid date: {path.name}, {day}")
            key = code, day
            if key in seen:
                raise ValueError(f"Duplicate stock/date: {key}")
            seen.add(key)
            raw = [row[key] for key in ("open", "high", "low", "close")]
            try:
                values = [Decimal(value) for value in raw]
                for text, value in zip(raw, values):
                    if (not re.fullmatch(r"[0-9]+(?:\.[0-9]{1,4})?", text)
                            or not value.is_finite() or value < 0
                            or value >= Decimal("1000000000000")
                            or value.as_tuple().exponent < -4):
                        raise ValueError("Price must fit nonnegative NUMERIC(16,4)")
                opening, high, low, close = values
                if not low <= min(opening, close) <= max(opening, close) <= high:
                    raise ValueError("Invalid OHLC range")
            except (InvalidOperation, ValueError) as error:
                raise ValueError(f"{path.name}, {day}: {error}") from error
            prices.append([code, day, *raw])
            priced_codes.add(code)
    if not prices or priced_codes != set(names):
        raise ValueError("Every mapped stock must have price data")
    prices.sort(key=lambda row: (row[0], row[1]))
    metadata = {
        "demo_data": False,
        "source": "使用者匯入 CSV（非即時行情）",
        "stocks": len(names), "prices": len(prices),
        "groups": len({group for group, _ in memberships}),
        "group_memberships": len(memberships),
        "earliest_date": min(row[1] for row in prices),
        "latest_available_date": max(row[1] for row in prices),
        "source_files": {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                         for path in group_files + price_files},
    }
    output_dir.mkdir(parents=True, exist_ok=True)
    for name, header, rows in (
        ("stocks.csv", ["code", "name"], sorted(names.items())),
        ("prices.csv", ["stock_code", "trading_date", "open_price", "high_price", "low_price", "close_price"], prices),
        ("groups.csv", ["group_name", "stock_code"], sorted(memberships)),
    ):
        with (output_dir / name).open("w", encoding="utf-8", newline="") as handle:
            writer = csv.writer(handle, lineterminator="\n")
            writer.writerow(header)
            writer.writerows(rows)
    (output_dir / "dataset.json").write_text(
        json.dumps(metadata, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({key: value for key, value in metadata.items() if key != "source_files"}, ensure_ascii=False))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input-dir", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    import_dataset(args.input_dir.resolve(), args.output_dir.resolve())

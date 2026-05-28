#!/usr/bin/env python3
import json
import os
import sys
import argparse
import time
from datetime import datetime

KANBAN_FILE = ".ariadne/kanban.json"

def load_board():
    if os.path.exists(KANBAN_FILE):
        with open(KANBAN_FILE, 'r') as f:
            return json.load(f)
    return {
        "todo": [],
        "in-progress": [],
        "done": []
    }

def save_board(board):
    os.makedirs(os.path.dirname(KANBAN_FILE), exist_ok=True)
    with open(KANBAN_FILE, 'w') as f:
        json.dump(board, f, indent=2)

def add_card(title):
    board = load_board()
    card_id = int(time.time() * 1000) % 10000
    card = {
        "id": str(card_id),
        "title": title,
        "created_at": datetime.now().isoformat()
    }
    board["todo"].append(card)
    save_board(board)
    print(f"Added card '{title}' with ID {card_id} to 'todo'")

def move_card(card_id, target_col):
    board = load_board()
    if target_col not in board:
        print(f"Error: Invalid column '{target_col}'")
        sys.exit(1)
    
    found_card = None
    for col in board:
        for i, card in enumerate(board[col]):
            if card["id"] == card_id:
                found_card = board[col].pop(i)
                break
        if found_card:
            break
    
    if not found_card:
        print(f"Error: Card ID {card_id} not found")
        sys.exit(1)
    
    board[target_col].append(found_card)
    save_board(board)
    print(f"Moved card {card_id} to '{target_col}'")

def list_cards():
    board = load_board()
    print("=== Ariadne Kanban Board ===")
    for col in ["todo", "in-progress", "done"]:
        print(f"\n[{col.upper()}]")
        if not board[col]:
            print("  (empty)")
        for card in board[col]:
            print(f"  {card['id']}: {card['title']}")

def delete_card(card_id):
    board = load_board()
    found = False
    for col in board:
        for i, card in enumerate(board[col]):
            if card["id"] == card_id:
                board[col].pop(i)
                found = True
                break
        if found:
            break
    
    if not found:
        print(f"Error: Card ID {card_id} not found")
        sys.exit(1)
    
    save_board(board)
    print(f"Deleted card {card_id}")

def main():
    parser = argparse.ArgumentParser(description="Manage Ariadne Kanban board")
    subparsers = parser.add_subparsers(dest="command")

    add_parser = subparsers.add_parser("add")
    add_parser.add_argument("title", help="Title of the card")

    move_parser = subparsers.add_parser("move")
    move_parser.add_argument("id", help="ID of the card")
    move_parser.add_argument("column", help="Target column")

    list_parser = subparsers.add_parser("list")

    delete_parser = subparsers.add_parser("delete")
    delete_parser.add_argument("id", help="ID of the card")

    args = parser.parse_args()

    if args.command == "add":
        add_card(args.title)
    elif args.command == "move":
        move_card(args.id, args.column)
    elif args.command == "list":
        list_cards()
    elif args.command == "delete":
        delete_card(args.id)
    else:
        parser.print_help()

if __name__ == "__main__":
    main()
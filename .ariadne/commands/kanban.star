name = "kanban"
description = "Visualize the local kanban board"

def run(args):
    path = ".ariadne/kanban.json"
    content = read_file(path)
    if not content:
        print("Kanban board is empty or missing.")
        return
    
    board = json.decode(content)
    
    print("\n\033[1mAriadne Kanban Board\033[0m")
    print("=====================")
    
    for col in ["todo", "in-progress", "done"]:
        title = col.upper()
        # Simple color for columns
        color = "\033[34m" # Blue
        if col == "in-progress":
            color = "\033[33m" # Yellow
        elif col == "done":
            color = "\033[32m" # Green
            
        print("\n" + color + "[" + title + "]\033[0m")
        cards = board.get(col, [])
        if not cards:
            print("  (empty)")
        for card in cards:
            print("  \033[90m" + card["id"] + ":\033[0m " + card["title"])
    print("")
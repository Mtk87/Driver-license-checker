# Driver licence checker
A small utility to do software based age verification and entrance control in go.
I put it online because I was looking for something simple to do age and access check for an event and couldn't find anything, I'm sure it can be written 1000 times better.

The utility was built with the help of AI.

Driver licensces are PDF417 coded barcodes in the United States.
If you are using the software as is, you have to set up your barcode reader to cut the first 5 digits (@ANSI) and about the last 20 digits.

On windows 11 it shows icons and colors correctly, may show bad on windows 10 and early.

It stores scanned driver license numbers in a json file: I used that because in my use case I needed to check that nobody has came to the same event for more than 2 times.

If you want to use it without compiling, the exe is attached.

Otherwise, you have to compile with 

go build -o LicenseReader.exe

<img width="1481" height="907" alt="image" src="https://github.com/user-attachments/assets/22ecba01-cb4b-44e3-9307-fccf111d7a53" />
